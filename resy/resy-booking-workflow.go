package resy

import (
	"errors"
	"fmt"
	"resy-snipe/config"
	"sync"
	"time"
)

type ResyBookingWorkflow struct {
	resyClient ResyClient
	resDetails config.ReservationDetails
}

func NewResyBookingWorkflow(resyClient ResyClient, resDetails config.ReservationDetails) *ResyBookingWorkflow {
	return &ResyBookingWorkflow{
		resyClient: resyClient,
		resDetails: resDetails,
	}
}

// Run searches for availability, retrying for up to retryFor, then attempts to
// book every matching slot concurrently. The first booking that succeeds wins
// and its reservation token is returned; if none succeed, every attempt's error
// is returned together.
func (r *ResyBookingWorkflow) Run(retryFor time.Duration) (string, error) {
	fmt.Printf("\nTaking the shot... ︻デ═一 *\n")

	configIds, err := r.Find(retryFor)
	if err != nil {
		return "", err
	}
	return r.Book(configIds)
}

// Find polls for availability, retrying for up to retryFor, and returns the
// config tokens of every matching slot. It books nothing, so it is safe to run
// against several targets at once.
func (r *ResyBookingWorkflow) Find(retryFor time.Duration) ([]string, error) {
	resQueue, err := r.resyClient.findReservations(r.resDetails.Date, r.resDetails.PartySize, r.resDetails.VenueId, r.resDetails.ResTimeTypes, retryFor)
	if err != nil {
		return nil, err
	}

	var configIds []string
	for e := resQueue.Front(); e != nil; e = e.Next() {
		if configId, ok := e.Value.(string); ok && configId != "" {
			configIds = append(configIds, configId)
		}
	}
	return configIds, nil
}

// Book attempts every supplied config token concurrently and returns the first
// booking that succeeds.
func (r *ResyBookingWorkflow) Book(configIds []string) (string, error) {
	if len(configIds) == 0 {
		return "", errors.New("no bookable reservation slots were found")
	}

	// booked, resyToken and errs are written from every booking goroutine, so
	// they are only ever touched while holding mu.
	var (
		mu        sync.Mutex
		booked    bool
		resyToken string
		errs      []error
	)

	var wg sync.WaitGroup
	for _, configId := range configIds {
		wg.Add(1)
		go func(configId string) {
			defer wg.Done()

			// Another slot got there first; don't book a second table.
			mu.Lock()
			claimed := booked
			mu.Unlock()
			if claimed {
				return
			}

			token, err := r.snipeConfigId(configId)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if !booked {
				booked = true
				resyToken = token
			}
		}(configId)
	}
	wg.Wait()

	if booked {
		return resyToken, nil
	}
	if len(errs) == 0 {
		return "", errors.New("no booking attempt completed")
	}
	return "", errors.Join(errs...)
}

func (r *ResyBookingWorkflow) snipeConfigId(configId string) (string, error) {
	bookingDetails, err := r.resyClient.getReservationDetails(configId, r.resDetails.Date, r.resDetails.PartySize)
	if err != nil {
		fmt.Printf("getReservationDetails error: %s\n", err)
		return "", err
	}
	return r.resyClient.BookReservation(bookingDetails.PaymentMethodID, bookingDetails.BookToken)
}
