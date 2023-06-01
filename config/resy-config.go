package config

type ResyKeys struct {
    ApiKey    string
    AuthToken string
}

type ReservationTimeType struct {
    ReservationTime string
    TableType       *string
}

type ReservationDetails struct {
    Date          string
    PartySize     int
    VenueId       int
    ResTimeTypes  []ReservationTimeType
}

type SnipeTime struct {
    Hours   int
    Minutes int
}

func NewReservationTimeType(reservationTime string, tableType *string) ReservationTimeType {
    return ReservationTimeType{ReservationTime: reservationTime, TableType: tableType}
}

var ResyKeyss = ResyKeys{ApiKey: "VbWk7s3L4KiK5fzlO7JD3Q5EYolJI7n5", AuthToken: "eyJ0eXAiOiJKV1QiLCJhbGciOiJFUzI1NiJ9.eyJleHAiOjE2ODY4MzgzODIsInVpZCI6NjA2NzA3OCwiZ3QiOiJjb25zdW1lciIsImdzIjpbXSwibGFuZyI6ImVuLXVzIiwiZXh0cmEiOnsiZ3Vlc3RfaWQiOjMwODc3NDAwfX0.AWZVaVaN3d3ivapUw-V7gxBSgA0exv0hLPW_lD3fkN9sqa2dB_bIOYhwzEwV7wsrj14XDpGm62i77OiNdmEARe1mAJpUzGk0LVa3ubxPCrWumh9l1cBvUXr_OV8rEwcixCbViwbyYatT6OM4It_ZfIRHneJOcDkeWxeUWYpF4kl_aP_w"}
var SnipeTimee = SnipeTime{Hours: 0, Minutes: 0}
// var tableType = "Dining Room"
var tableType = "Taproom Table"
var ResTimeTypes = []ReservationTimeType{
    NewReservationTimeType("12:15:00", &tableType),
    NewReservationTimeType("12:00:00", nil),
    NewReservationTimeType("12:30:00", nil),
    // NewReservationTimeType("19:15:00", nil),
    // NewReservationTimeType("19:30:00", nil),
    // NewReservationTimeType("18:30:00", nil),
    // NewReservationTimeType("18:30:00", &tableType),
}

// DeadRabbit: 38660
// Rubirosa: 466
var ReservationDetailss = ReservationDetails{
    Date:         "2023-06-04",
    PartySize:    2,
    VenueId:      38660,
    ResTimeTypes: ResTimeTypes,
}