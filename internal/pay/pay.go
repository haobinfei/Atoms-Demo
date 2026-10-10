// Package pay is the 模拟收银台 (mock checkout) domain: 订单 (Order)
// records for generated ecommerce apps. Orders persist per session on
// disk so a shopper on another device or browser sees the same history.
// Only the platform's test card is accepted and every page says so —
// no real money moves here. Mock semantics include trusting each item's
// unit price from the client: there is no price registry, and nothing is
// actually charged.
package pay

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Item is one line of an 订单 (Order).
type Item struct {
	Name  string  `json:"name"`
	Price float64 `json:"price"`
	Qty   int     `json:"qty"`
}

// LineTotal is price × qty, rounded like Amount.
func (it Item) LineTotal() float64 {
	return math.Round(it.Price*float64(it.Qty)*100) / 100
}

// PriceString / QtyString / LineString render the line for the checkout
// page without trailing zeros ("28", not "28.00").
func (it Item) PriceString() string { return trimFloat(it.Price) }
func (it Item) QtyString() string   { return strconv.Itoa(it.Qty) }
func (it Item) LineString() string  { return trimFloat(it.LineTotal()) }

// Order is one purchase against one generated app. Amount is always
// computed server-side from Items, never trusted from the client.
type Order struct {
	ID        string     `json:"id"`
	App       string     `json:"app"`
	Items     []Item     `json:"items"`
	Amount    float64    `json:"amount"`
	Status    string     `json:"status"` // pending | paid
	CardLast4 string     `json:"cardLast4,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	PaidAt    *time.Time `json:"paidAt,omitempty"`
}

// AmountString renders the total without trailing zeros.
func (o Order) AmountString() string { return trimFloat(o.Amount) }

func trimFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// maxItems bounds one order so a malicious client cannot grow the order
// book without limit.
const maxItems = 100

var (
	ErrEmptyItems  = errors.New("items must not be empty")
	ErrBadItem     = errors.New("item name/price/qty invalid")
	ErrNotFound    = errors.New("order not found")
	ErrWrongCard   = errors.New("only the test card 4242 4242 4242 4242 is accepted")
	ErrAlreadyPaid = errors.New("order already paid")
)

// TestCard is the only card the mock checkout accepts, and the only one
// the checkout page pre-fills.
const TestCard = "4242 4242 4242 4242"

// Store persists orders as one JSON file per session workspace. A single
// mutex guards every read-modify-write cycle, mirroring the session
// store's locking discipline.
type Store struct {
	mu sync.Mutex
}

func New() *Store { return &Store{} }

func ordersFile(dir string) string { return filepath.Join(dir, "orders.json") }

// Create validates the items, computes the amount and appends a pending
// order to the session's order book.
func (s *Store) Create(dir, app string, items []Item) (*Order, error) {
	if len(items) == 0 || len(items) > maxItems {
		return nil, ErrEmptyItems
	}
	amount := 0.0
	for _, it := range items {
		if strings.TrimSpace(it.Name) == "" || it.Price < 0 || it.Qty < 1 {
			return nil, fmt.Errorf("%w: %q", ErrBadItem, it.Name)
		}
		amount += it.Price * float64(it.Qty)
	}
	amount = math.Round(amount*100) / 100

	id, err := newID()
	if err != nil {
		return nil, err
	}
	order := &Order{
		ID:        id,
		App:       app,
		Items:     items,
		Amount:    amount,
		Status:    "pending",
		CreatedAt: time.Now(),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	orders, err := load(dir)
	if err != nil {
		return nil, err
	}
	orders = append(orders, *order)
	if err := save(dir, orders); err != nil {
		return nil, err
	}
	return order, nil
}

// List returns the session's order book, oldest first.
func (s *Store) List(dir string) ([]Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return load(dir)
}

// Pay marks one order paid, but only for the test card (spaces optional).
func (s *Store) Pay(dir, id, card string) (*Order, error) {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, card)
	if digits != "4242424242424242" {
		return nil, ErrWrongCard
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	orders, err := load(dir)
	if err != nil {
		return nil, err
	}
	for i := range orders {
		if orders[i].ID != id {
			continue
		}
		if orders[i].Status == "paid" {
			return nil, ErrAlreadyPaid
		}
		now := time.Now()
		orders[i].Status = "paid"
		orders[i].PaidAt = &now
		orders[i].CardLast4 = digits[len(digits)-4:]
		if err := save(dir, orders); err != nil {
			return nil, err
		}
		paid := orders[i]
		return &paid, nil
	}
	return nil, ErrNotFound
}

func load(dir string) ([]Order, error) {
	raw, err := os.ReadFile(ordersFile(dir))
	if errors.Is(err, os.ErrNotExist) {
		return []Order{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read orders: %w", err)
	}
	var orders []Order
	if err := json.Unmarshal(raw, &orders); err != nil {
		return nil, fmt.Errorf("parse orders: %w", err)
	}
	return orders, nil
}

func save(dir string, orders []Order) error {
	raw, err := json.MarshalIndent(orders, "", "  ")
	if err != nil {
		return err
	}
	tmp := ordersFile(dir) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("write orders: %w", err)
	}
	return os.Rename(tmp, ordersFile(dir))
}

func newID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
