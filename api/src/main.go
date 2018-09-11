package main

import (
	"github.com/jackc/pgx"
	"github.com/julienschmidt/httprouter"
	"golang.org/x/net/context"
	"googlemaps.github.io/maps"

	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"
)

var (
	maxRetries   = 10
	retryTimeout = 5
)

func main() {
	// db setup
	DBURI := os.Getenv("DB_URI")
	config, err := pgx.ParseConnectionString(DBURI)
	if err != nil {
		log.Print("Error in parsing connection string: ", err)
		os.Exit(2)
	}

	// connect to db
	conn, err := pgx.Connect(config)
	for err != nil {
		if maxRetries == 0 {
			log.Printf("Error in connecting to db: %s\nShutting down.", err)
			os.Exit(2)
		}
		// retry
		log.Printf("Error in connecting to db: %s\nRetrying in %d seconds...", err, retryTimeout)
		time.Sleep(time.Duration(retryTimeout) * time.Second)
		conn, err = pgx.Connect(config)
		maxRetries -= 1
	}
	log.Println("Connected to DB")

	// maps setup
	mapsClient, err := maps.NewClient(maps.WithAPIKey("AIzaSyDoVYIjaY--YgsE5gZHk8AZMI_-qD4FO-Q"))
	log.Println("Connected to Google Maps Service")
	s := Services{
		DB:   conn,
		Maps: mapsClient,
	}

	// api setup
	router := httprouter.New()

	router.POST("/order", s.placeOrderHandler)
	router.PUT("/order/:id", s.takeOrderHandler)
	router.GET("/orders", s.listOrderHandler)

	log.Println("Listening at :8080")
	log.Fatal(http.ListenAndServe(":8080", router))
}

type Error struct {
	Error string `json:"error"`
}

type Status struct {
	Status string `json:"status"`
}

type Location struct {
	Origin      [2]string `json:"origin"` // assumes [lat, lng]
	Destination [2]string `json:"destination"`
}

func (loc *Location) toDistanceMatrixRequest() *maps.DistanceMatrixRequest {
	dmr := &maps.DistanceMatrixRequest{
		Origins:       []string{fmt.Sprintf("%s,%s", loc.Origin[0], loc.Origin[1])},
		Destinations:  []string{fmt.Sprintf("%s,%s", loc.Destination[0], loc.Destination[1])},
		DepartureTime: "now",
		Mode:          maps.TravelModeDriving,
	}
	return dmr
}

type Order struct {
	Id       int
	Distance float64
	Is_taken bool
}

func (order *Order) toResponse() OrderResponse {
	or := &OrderResponse{
		Id:       order.Id,
		Distance: order.Distance,
		Status:   "UNASSIGN",
	}
	if order.Is_taken == true {
		or.Status = "taken" // not sure why lowercase in spscs
	}
	return *or
}

type OrderResponse struct {
	Id       int     `json:"id"`
	Distance float64 `json:"distance"`
	Status   string  `json:"status"`
}

type Services struct {
	DB   *pgx.Conn
	Maps *maps.Client
}

func ErrorBadRequest(
	w http.ResponseWriter,
	err interface{},
) {
	log.Println(err)

	blob, _ := json.Marshal(&Error{"Bad Request"})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(400)
	w.Write(blob)
}

func ErrorInternalServer(
	w http.ResponseWriter,
	err interface{},
) {
	log.Println(err)

	blob, _ := json.Marshal(&Error{"Internal Server Error"})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(500)
	w.Write(blob)
}

func ErrorDatabase(
	w http.ResponseWriter,
	err interface{},
) {
	log.Println(err)

	blob, _ := json.Marshal(&Error{"Database Error"})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(500)
	w.Write(blob)
}

func ErrorJSONMarshal(
	w http.ResponseWriter,
	err interface{},
) {
	log.Println(err)

	blob, _ := json.Marshal(&Error{"JSON Marshalling Error"})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(500)
	w.Write(blob)
}

func ErrorNotFound(
	w http.ResponseWriter,
	err interface{},
) {
	log.Println(err)

	blob, _ := json.Marshal(&Error{"Not Found"})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(404)
	w.Write(blob)
}

func (s *Services) placeOrderHandler(
	w http.ResponseWriter,
	req *http.Request,
	_ httprouter.Params,
) {
	// assert request header
	// but not included in specs sooooo won't add :(
	// if req.Header.Get("Content-Type") != "application/json" {
	// 	ErrorBadRequest(w, "Invalid content type")
	// 	return
	// }

	// read []byte
	bodyBlob, err := ioutil.ReadAll(req.Body)
	if err != nil {
		ErrorBadRequest(w, err)
		return
	}

	// convert []byte to struct
	var loc Location
	err = json.Unmarshal(bodyBlob, &loc)
	if err != nil {
		ErrorBadRequest(w, err)
		return
	}

	// assert required values
	if len(loc.Origin) < 2 || len(loc.Destination) < 2 {
		ErrorBadRequest(w, "Invalid parameters")
		return
	}

	// get distance
	// l := &Location{[2]string{"22.3376459", "114.1474979"}, [2]string{"22.3292858", "114.1470621"}}
	distMatrixResp, err := s.Maps.DistanceMatrix(
		context.Background(),
		loc.toDistanceMatrixRequest(),
	)
	if err != nil {
		ErrorInternalServer(w, err)
		return
	}
	// assumes the first row and element contains the right distance
	// log.Println(l, distMatrixResp, distMatrixResp.Rows[0].Elements[0].Distance.Meters)
	distance := distMatrixResp.Rows[0].Elements[0].Distance.Meters
	if distance == 0 {
		ErrorBadRequest(w, err)
		return
	}

	// log the order to db
	var o Order
	err = s.DB.
		QueryRow("INSERT INTO delivery_order VALUES(DEFAULT, $1, DEFAULT) RETURNING *", distance).
		Scan(&o.Id, &o.Distance, &o.Is_taken)
	if err != nil {
		ErrorDatabase(w, err)
		return
	}

	// marshal response
	blob, err := json.Marshal(o.toResponse())
	if err != nil {
		ErrorJSONMarshal(w, err)
		return
	}

	// write response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(blob)
	return
}

func (s *Services) takeOrderHandler(
	w http.ResponseWriter,
	req *http.Request,
	params httprouter.Params,
) {
	// assert request header
	// but not included in specs sooooo won't add :(
	// if req.Header.Get("Content-Type") != "application/json" {
	// 	ErrorBadRequest(w, "Invalid content type")
	// 	return
	// }

	// read []byte
	bodyBlob, err := ioutil.ReadAll(req.Body)
	if err != nil {
		ErrorBadRequest(w, err)
		return
	}

	// convert []byte to struct
	var status Status
	err = json.Unmarshal(bodyBlob, &status)
	if err != nil {
		ErrorBadRequest(w, err)
		return
	}

	// assert required values
	id, err := strconv.ParseInt(params[0].Value, 10, 64)
	if err != nil || status.Status != "taken" {
		ErrorBadRequest(w, "Invalid parameters")
		return
	}

	// check if order in db
	var taken bool
	err = s.DB.
		QueryRow("SELECT is_taken FROM delivery_order WHERE id = $1", id).
		Scan(&taken)
	if err != nil && err == pgx.ErrNoRows {
		ErrorNotFound(w, err)
		return
	}
	if err != nil {
		ErrorDatabase(w, err)
		return
	}

	// return 409 if taken
	if taken {
		blob, _ := json.Marshal(&Error{"ORDER_ALREADY_BEEN_TAKEN"})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(409)
		w.Write(blob)
		return
	}

	_, err = s.DB.Exec("UPDATE delivery_order SET is_taken = true WHERE id = $1", id)
	if err != nil {
		ErrorDatabase(w, err)
		return
	}
	// write response
	blob, _ := json.Marshal(&Status{"SUCCESS"})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(blob)
	return
}

func (s *Services) listOrderHandler(
	w http.ResponseWriter,
	req *http.Request,
	_ httprouter.Params,
) {
	// read query params
	err := req.ParseForm()
	if err != nil {
		ErrorBadRequest(w, "Malformed request")
		return
	}

	// assert required values
	page, err := strconv.ParseInt(req.Form.Get("page"), 10, 64)
	if err != nil || page < 0 {
		ErrorBadRequest(w, "Invalid parameters")
		return
	}
	limit, err := strconv.ParseInt(req.Form.Get("limit"), 10, 64)
	if err != nil || limit <= 0 || limit > 1000 {
		ErrorBadRequest(w, "Invalid parameters")
		return
	}

	// get orders from db
	// standard is to get items in reverse chronological order
	// (newest first) but there's no indication in the specs
	// for that plus I need to add in a created_at field in
	// the db to accommodate it
	var orders []OrderResponse
	rows, err := s.DB.
		Query("SELECT * FROM delivery_order LIMIT $1 OFFSET $2", limit, limit*page)

	for rows.Next() {
		var order Order
		err := rows.Scan(&order.Id, &order.Distance, &order.Is_taken)
		if err != nil {
			ErrorDatabase(w, err)
			return
		}
		orders = append(orders, order.toResponse())
	}

	sort.Slice(orders, func(i, j int) bool {
		return orders[i].Id < orders[j].Id
	})

	// write response
	blob, err := json.Marshal(orders)
	if err != nil {
		ErrorJSONMarshal(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write(blob)
	return
}
