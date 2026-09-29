package resy

import (
	// "os"
	"container/list"
	"encoding/json"
	"errors"
	"fmt"
	"resy-snipe/config"
	"strconv"
	"strings"
	"time"
)

// retryInterval paces the availability polling loop. Anything much faster
// trips Resy's rate limiter (429) within seconds, which then rejects the
// booking calls that actually matter.
const retryInterval = 1 * time.Second

type ReservationMap map[string]TableTypeMap
type TableTypeMap map[string]string

// ResyAPIClient is the subset of ResyAPI that ResyClient depends on. It exists
// so the booking flow can be exercised against a fake in tests.
type ResyAPIClient interface {
	GetReservations(date string, partySize int, venueID int) (string, error)
	GetReservationDetails(configID string, date string, partySize int) (string, error)
	PostReservation(paymentMethodID string, bookToken string) (string, error)
	SearchVenues(query string) (string, error)
	CreateNotify(venueID int, date string, partySize int, startTime string, endTime string) (string, error)
}

type ResyClient struct {
	resyApi ResyAPIClient
}

type BookingDetails struct {
	PaymentMethodID string
	BookToken       string
}

// Define a struct that matches the structure of the JSON data
type Response struct {
	Results struct {
		Venues []struct {
			Slots []struct {
				Config struct {
					Type  string `json:"type"`
					Token string `json:"token"`
				} `json:"config"`
				Date struct {
					Start string `json:"start"`
				} `json:"date"`
			} `json:"slots"`
		} `json:"venues"`
	} `json:"results"`
}

func NewResyClient(resyApi ResyAPIClient) *ResyClient {
	return &ResyClient{resyApi: resyApi}
}

func getMapFirstKey(tableTypeMap TableTypeMap) string {
	var firstKey string
	for key, _ := range tableTypeMap {
		firstKey = key
		break
	}
	return firstKey
}

// findReservations polls for availability until a matching slot shows up or
// retryFor elapses, whichever comes first. A retryFor of 0 means a single
// attempt.
func (rc *ResyClient) findReservations(date string, partySize int, venueId int, resTimeTypes []config.ReservationTimeType, retryFor time.Duration) (*list.List, error) {
	if len(resTimeTypes) == 0 {
		return nil, errors.New("no reservation time/table type combinations were requested")
	}

	deadline := time.Now().Add(retryFor)
	for {
		queue, err := rc.findOnce(date, partySize, venueId, resTimeTypes)
		if err == nil && queue.Len() > 0 {
			return queue, nil
		}
		if err != nil {
			// A transient failure (rate limit, network blip) must not abort the
			// hunt while retry budget remains.
			fmt.Printf("availability check failed: %v\n", err)
		} else {
			fmt.Println("No Hits")
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("could not find a reservation for %s (party of %d) within %s", date, partySize, retryFor)
		}
		if remaining < retryInterval {
			time.Sleep(remaining)
		} else {
			time.Sleep(retryInterval)
		}
	}
}

// findOnce runs a single availability lookup and returns the config tokens
// matching resTimeTypes. An empty queue means "no hits", not an error.
func (rc *ResyClient) findOnce(date string, partySize int, venueId int, resTimeTypes []config.ReservationTimeType) (*list.List, error) {
	reservations, err := rc.Availability(date, partySize, venueId)
	if err != nil {
		return nil, err
	}
	return matchConfigIDs(reservations, resTimeTypes), nil
}

// Availability returns every slot the venue is currently offering, keyed by
// start time and then table type. It books nothing, so it is also what the
// dry run reports.
func (rc *ResyClient) Availability(date string, partySize int, venueId int) (ReservationMap, error) {
	resp, err := rc.resyApi.GetReservations(date, partySize, venueId)
	if err != nil {
		return nil, err
	}

	// Unmarshal the JSON into the structured Go type
	var extractedValues Response
	if err := json.Unmarshal([]byte(resp), &extractedValues); err != nil {
		return nil, err
	}

	// Collect the offered slots as start time -> table type -> config token
	var reservations = make(ReservationMap)
	for _, venue := range extractedValues.Results.Venues {
		for _, slot := range venue.Slots {
			// Start looks like "2026-09-14 19:00:00"; skip anything malformed
			// rather than indexing past the end of the split.
			fields := strings.Split(slot.Date.Start, " ")
			if len(fields) < 2 {
				continue
			}
			start := fields[1]
			tableType := slot.Config.Type
			configID := slot.Config.Token

			if _, ok := reservations[start]; !ok {
				reservations[start] = TableTypeMap{
					tableType: configID,
				}
			} else {
				reservations[start][tableType] = configID
			}
		}
	}

	return reservations, nil
}

// matchConfigIDs picks the config tokens matching the requested times and table
// types, in the order they were requested.
func matchConfigIDs(reservations ReservationMap, resTimeTypes []config.ReservationTimeType) *list.List {
	queue := list.New()
	for _, r := range resTimeTypes {
		tableTypeMap, ok := reservations[r.ReservationTime]
		if !ok {
			continue
		}

		var configID string
		if r.TableType != nil {
			configID = tableTypeMap[*r.TableType]
		} else {
			configID = tableTypeMap[getMapFirstKey(tableTypeMap)]
		}

		// A requested table type the venue did not offer leaves an empty token
		// behind. Queueing it would count as a hit and stop the retry loop.
		if configID != "" {
			queue.PushBack(configID)
		}
	}
	return queue
}

// Get details of the reservation
// configId: Unique identifier for the reservation
// date: Date of the reservation in YYYY-MM-DD format
// partySize: Size of the party reservation
// returns the paymentMethodId and the bookingToken of the reservation
func (rc *ResyClient) getReservationDetails(configId string, date string, partySize int) (*BookingDetails, error) {
	resp, err := rc.resyApi.GetReservationDetails(configId, date, partySize)
	if err != nil {
		return nil, err
	}

	var resDetails map[string]interface{}
	respBytes := []byte(resp)
	if err := json.Unmarshal(respBytes, &resDetails); err != nil {
		return nil, err
	}

	// Searching this JSON structure...
	// {"user": {"payment_methods": [{"id": 42, ...}]}}
	paymentMethod := resDetails["user"].(map[string]interface{})["payment_methods"].([]interface{})[0].(map[string]interface{})
	paymentMethodId := strconv.Itoa(int(paymentMethod["id"].(float64)))
	// Searching this JSON structure...
	// {"book_token": {"value": "BOOK_TOKEN", ...}}
	bookToken := resDetails["book_token"].(map[string]interface{})["value"].(string)[0:len(resDetails["book_token"].(map[string]interface{})["value"].(string))]
	return &BookingDetails{
		PaymentMethodID: paymentMethodId,
		BookToken:       bookToken,
	}, nil
}

// BookReservation books the reservation
// paymentMethodId: unique identifier of the payment id in case of a late cancellation fee
// bookToken: unique identifier of the reservation in question
// returns: unique identifier of the confirmed booking
func (rc *ResyClient) BookReservation(paymentMethodID string, bookToken string) (string, error) {
	resp, err := rc.resyApi.PostReservation(paymentMethodID, bookToken)
	if err != nil {
		return "", err
	}

	var resyToken string
	resyToken = resp

	fmt.Println("Headshot -- successfully sniped reservation!")
	fmt.Println("(҂‾ ▵‾)︻デ═一 (× _ ×#")

	return resyToken, nil
}
