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

// VbWk7s3L4KiK5fzlO7JD3Q5EYolJI7n5
// 0aQz|8yOPIq|90KX8xi4998PEIUIMty7YTk3gZN|SkBkRUgDbAKHY9apqYYlNEqa6wVsn0mhiGRuipvtWebc|AwDPeaOnM6G2qZ89JZaiY0=-62d925c118a3c1f86e02c4c32f31c36005b7f4d93c22889888ec8063
// var ResyKeyss = ResyKeys{ApiKey: "VbWk7s3L4KiK5fzlO7JD3Q5EYolJI7n5", AuthToken: "eyJ0eXAiOiJKV1QiLCJhbGciOiJFUzI1NiJ9.eyJleHAiOjE2ODY4MzgzODIsInVpZCI6NjA2NzA3OCwiZ3QiOiJjb25zdW1lciIsImdzIjpbXSwibGFuZyI6ImVuLXVzIiwiZXh0cmEiOnsiZ3Vlc3RfaWQiOjMwODc3NDAwfX0.AWZVaVaN3d3ivapUw-V7gxBSgA0exv0hLPW_lD3fkN9sqa2dB_bIOYhwzEwV7wsrj14XDpGm62i77OiNdmEARe1mAJpUzGk0LVa3ubxPCrWumh9l1cBvUXr_OV8rEwcixCbViwbyYatT6OM4It_ZfIRHneJOcDkeWxeUWYpF4kl_aP_w"}
var ResyKeyss = ResyKeys{ApiKey: "VbWk7s3L4KiK5fzlO7JD3Q5EYolJI7n5", AuthToken: "eyJ0eXAiOiJKV1QiLCJhbGciOiJFUzI1NiJ9.eyJleHAiOjE2OTI5NTkwMDEsInVpZCI6NjA2NzA3OCwiZ3QiOiJjb25zdW1lciIsImdzIjpbXSwibGFuZyI6ImVuLXVzIiwiZXh0cmEiOnsiZ3Vlc3RfaWQiOjMwODc3NDAwfX0.AWj6v3V5YE1Yy9VbHTAsAyto4djh8cmBdFeurJ12IoHsex6tyrmqbHndAI0S2CGmOuYclZJ7KqgCHepY5SL-kfIIAHM2SEY3ZiYwp-KIjPeVg6HOqppddqUSbbNZbAgW1qnpwt67x4y0K15RENyk_b1QyUyoyuqCnUvtFIjCkiPIkBsT"}
var SnipeTimee = SnipeTime{Hours: 0, Minutes: 0}
// var tableType = "Dining Room"
// var tableType = "Taproom Table"
var ResTimeTypes = []ReservationTimeType{
    NewReservationTimeType("19:00:00", nil),
    NewReservationTimeType("18:45:00", nil),
    NewReservationTimeType("19:15:00", nil),
    NewReservationTimeType("18:30:00", nil),
    NewReservationTimeType("18:15:00", nil),
    NewReservationTimeType("18:00:00", nil),
    NewReservationTimeType("19:45:00", nil),
    NewReservationTimeType("17:45:00", nil),
}

// DeadRabbit: 38660
// Rubirosa: 466
// Red Pearl: 69820
// Raf's: 65679
var ReservationDetailss = ReservationDetails{
    Date:         "2023-07-22",
    PartySize:    4,
    VenueId:      466,
    ResTimeTypes: ResTimeTypes,
}