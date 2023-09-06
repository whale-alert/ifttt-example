package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dustin/go-humanize"
	"golang.org/x/time/rate"
	"log"
	"net/http"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	// REPLACE THESE WITH YOUR OWN VALUES
	whaleAlertApiKey = "YOUR_WHALE_ALERT_API_KEY" // Your Whale Alert API key
	iftttApiKey      = "YOUR_IFTTT_API_KEY" // Your IFTTT API key

	iftttWebhookEventName = "whale_alert"
)

var (
	// The alert subscription message
	// EDIT THESE VALUES TO SUBSCRIBE TO DIFFERENT ALERTS
	subscription = AlertSubscriptionJSON{
		Type:        "subscribe_alerts", // Use "subscribe_socials" to receive the alerts that Whale Alert posts on social media. The blockchains, symbols, types and min usd value fields are ignored when subscribing to socials
		ID:          "my_subscription",
		Blockchains: []string{ // Add the blockchains you want to subscribe to or leave empty to subscribe to all blockchains
			//"ethereum",
		},
		Symbols: []string{ // Add the symbols you want to subscribe to or leave empty to subscribe to all symbols
			//"eth",
		},
		Types: []string{ // Add the transaction types you want to subscribe to or leave empty to subscribe to all transaction types
			"transfer",
			"mint",
			"burn",
			"freeze",
			"unfreeze",
			"lock",
			"unlock",
		},
		MinValueUSD: 10_000_000, // The minimum transaction value in USD to trigger an alert
	}

	iftttRateLimit, iftttRateBurst = rate.Every(time.Minute * 5), 5 // Limit sending more than 1 alert per 5 minute with a burst of 5 to IFTTT                                                       // The rate limit for IFTTT webhooks
)

const (
	iftttWebhookURL     = "https://maker.ifttt.com/trigger/%s/with/key/%s"      // The IFTTT webhook URL
	iftttWebhookJSONURL = "https://maker.ifttt.com/trigger/%s/json/with/key/%s" // The IFTTT webhook URL when using a "Receive a web request with a JSON payload" webhook
	connectTimeout      = time.Minute * 1                                       // The timeout for connecting to the WebSocket server
)

func main() {
	// Create a new WebSocket instance
	ws := &WebSocket{
		iftttRateLimiter: rate.NewLimiter(iftttRateLimit, iftttRateBurst), // Limit the number of IFTTT webhook calls
		quit:             make(chan struct{}),
	}
	defer func() {
		ws.Close() // Close the WebSocket connection when the main goroutine is finished
		log.Println("Closed WebSocket Client")
	}()

	// Connect to the WebSocket server
	ws.wg.Add(1)
	go func() {
		defer ws.wg.Done() // Signal done when this goroutine is finished

		// Keep connecting to the WebSocket server until we receive the quit signal
		for {
			select {
			case <-ws.quit:
				return // Exit the connect/read loop when the quit signal is received
			default:
			}

			// Connect to the WebSocket server
			if err := ws.connect(); err != nil {
				log.Println("error connecting to websocket:", err)
				ws.pause()
				continue
			}

			// Send the alert subscription message
			if err := ws.subscribeAlerts(); err != nil {
				log.Println("error connecting to websocket:", err)
				ws.pause()
				continue
			}

			// Keep reading messages from the WebSocket server until we receive the quit signal or the connection is lost
			if err := ws.readLoop(); err != nil {
				log.Println("error reading from websocket:", err)
				ws.pause()
			}
		}
	}()

	log.Println("WebSocket Client Started")

	// Catches system signal.
	chSig := make(chan os.Signal, 1)
	signal.Notify(chSig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	<-chSig

	log.Println("Closing WebSocket Client")
}

// WebSocket represents a WebSocket connection
type WebSocket struct {
	conn *websocket.Conn // The WebSocket connection

	iftttRateLimiter *rate.Limiter // Used to limit the number of IFTTT webhook calls

	wg   sync.WaitGroup // Used to wait for the readLoop goroutine to finish
	quit chan struct{}  // Used to signal the readLoop goroutine to stop
}

// Close closes the WebSocket connection
func (w *WebSocket) Close() {
	// Close the WebSocket connection
	_ = w.conn.Close(websocket.StatusNormalClosure, "") // Close the WebSocket connection

	// Send the close signal to all goroutines
	close(w.quit)

	// Wait for all goroutines to finish
	w.wg.Wait()

	fmt.Println("Closed WebSocket connection. Wait")
}

// AlertSubscriptionJSON represents the JSON message sent to the WebSocket server to subscribeAlerts to alerts
// Also returned by the WebSocket server when the subscription was successful
type AlertSubscriptionJSON struct {
	Type        string   `json:"type"`                    // The type of the message (subscribe_alerts, subscribe_socials, subscribed_alerts, subscribed_socials)
	ID          string   `json:"id,omitempty"`            // The ID of the subscription
	Blockchains []string `json:"blockchains,omitempty"`   // The blockchains to subscribeAlerts to (lowercase)
	Symbols     []string `json:"symbols,omitempty"`       // The symbols to subscribeAlerts to (lowercase)
	Types       []string `json:"tx_types,omitempty"`      // The transaction types to subscribeAlerts to (transfer, mint, burn, freeze, unfreeze, lock, unlock)
	MinValueUSD float64  `json:"min_value_usd,omitempty"` // The minimum transaction value in USD to trigger an alert
}

type SocialsJSON struct {
	Type       string   `json:"type"`         // The type of the message
	ID         string   `json:"id,omitempty"` // The ID of the subscription
	Timestamp  int      `json:"timestamp"`    // The timestamp of the transaction
	Blockchain string   `json:"blockchain"`   // The blockchain this transaction was created on
	Text       string   `json:"text"`         // A human-readable representation of the transaction, similar to the Whale Alerts posted on our Twitter channel
	URLS       []string `json:"urls"`         // Direct links to the social media posts that were made
}

// AlertJSON represents an alert received from the WebSocket server
type AlertJSON struct {
	Type            string     `json:"type"`             // The type of the message
	ID              string     `json:"channel_id"`       // The ID of the subscription
	Timestamp       int        `json:"timestamp"`        // The timestamp of the transaction
	Blockchain      string     `json:"blockchain"`       // The blockchain this transaction was created on
	TransactionType string     `json:"transaction_type"` // The type of the transaction (transfer, mint, burn, freeze, unfreeze, lock, unlock)
	From            string     `json:"from"`             // The owner of the from-address(es) that initiated the transaction
	To              string     `json:"to"`               // The owner of the to-address(es) that are the receiver(s)
	Amounts         []struct { // The amounts transferred by the initiator of the transaction. A single transaction can have multiple symbols and amounts.
		Symbol   string  `json:"symbol"`    // The symbol of the currency (e.g. eth)
		Amount   float64 `json:"amount"`    // The amount of tokens
		ValueUsd float64 `json:"value_usd"` // The USD value of the amount
	} `json:"amounts"`
	Text        string          `json:"text"`        // A human-readable representation of the transaction, similar to the Whale Alerts posted on our Twitter channel
	Transaction TransactionJSON `json:"transaction"` // Complete transaction data
}

// TransactionJSON represents the transaction data of an alert
type TransactionJSON struct {
	BlockHeight     uint64           `json:"height"`                     // The block height at which the transaction was included
	IndexInBlock    int              `json:"index_in_block"`             // The index at which the transaction can be found in the block. The transaction index may not correspond to the index in the raw block data for certain blockchains
	Timestamp       int64            `json:"timestamp"`                  // The UNIX timestamp of the block containing the transaction
	Hash            string           `json:"hash"`                       // The hash of the transaction
	Fee             string           `json:"fee,omitempty"`              // The amount paid by the initiator of the transaction
	FeeSymbol       string           `json:"fee_symbol,omitempty"`       // The currency in which the fee was paid
	FeeSymbolPrice  float64          `json:"fee_symbol_price,omitempty"` // The price in USD per single unit of the currency at the block time
	SubTransactions []SubTransaction `json:"sub_transactions"`           // Every transaction can have any number of sub-transactions that change the balance of address(es) for a certain symbol
}

// SubTransaction represents a sub transaction of a transaction. Every transaction can have any number of sub-transactions that change the balance of address(es) for a certain symbol
type SubTransaction struct {
	Symbol          string    `json:"symbol"`                   // The symbol of the currency
	Price           float64   `json:"unit_price_usd,omitempty"` // The price in USD per single unit of the currency at the block time
	TransactionType string    `json:"transaction_type"`         // The type of this sub-transaction (transfer, mint, burn, freeze, unfreeze, lock, unlock)
	Inputs          []Address `json:"inputs"`                   // The inputs of a transaction or the FROM
	Outputs         []Address `json:"outputs"`                  // The outputs of a transaction or the TO
}

// Address represents an amount of a sub transaction
type Address struct {
	Amount      string `json:"amount"`                 // The amount of currency by which the address balance is altered
	Address     string `json:"address,omitempty"`      // The hash of the address
	Balance     string `json:"balance,omitempty"`      // The balance of the address after the transaction was concluded. Note this is a string type instead of a float to prevent issues related to precision
	Locked      string `json:"locked,omitempty"`       // The amount locked at the address. This amount cannot be transferred by the owner of the address
	IsFrozen    bool   `json:"is_frozen,omitempty"`    // True if the address has been frozen. A frozen address cannot transfer any current or future balance of this address
	Owner       string `json:"owner,omitempty"`        // The entity to which the address has been attributed. Empty when no owner data is available
	OwnerType   string `json:"owner_type,omitempty"`   // The type of the entity to which the address has been attributed (e.g. exchange)
	AddressType string `json:"address_type,omitempty"` // The type of the address. Possible values: deposit_wallet, hot_wallet, cold_wallet, exchange_wallet, fraud_deposit_address, hack_deposit_address, blackmail_deposit_address, theft_deposit_address, burn_address, coinbase_address, coinjoin_address, change_address, premine_address, donation_address, treasury_address, mixer_wallet, merchant_wallet
}

// readLoop keeps reading messages from the WebSocket server until the quit signal is received
func (w *WebSocket) readLoop() error {
	// Used to determine the message type received from the WebSocket server
	type Msg struct {
		Type string `json:"type"`
	}

	// Start reading messages
	for {
		select {
		case <-w.quit:
			return errors.New("quit signal received")
		default:
		}

		// Read a message from the WebSocket server
		_, buf, err := w.conn.Read(context.Background())
		if err != nil {
			return err
		}

		// Determine the type of the message
		var msg Msg
		if err := json.Unmarshal(buf, &msg); err != nil {
			log.Println("error unmarshalling message:", err)
			continue
		}

		// Handle the message based on its type
		switch msg.Type {
		case "subscribed_alerts":
			// Received confirmation of alert subscription
			var sub AlertSubscriptionJSON
			if err := json.Unmarshal(buf, &sub); err != nil {
				log.Println("error unmarshalling alert:", err)
				continue
			}

			log.Printf("Subscribed to alerts: Blockchains: %s, Symbols: %s, Transaction Types: %s, Min USD Value: %s", strings.Join(sub.Blockchains, ", "), strings.Join(sub.Symbols, ", "), strings.Join(sub.Types, ", "), humanize.Comma(int64(sub.MinValueUSD)))
		case "subscribed_socials":
			// Received confirmation of a subscription to socials
			log.Println("Subscribed to socials")
		case "alert":
			// Received a new custom alert
			var alert AlertJSON
			if err := json.Unmarshal(buf, &alert); err != nil {
				log.Println("error unmarshalling alert:", err)
				continue
			}

			w.postToWebhook(createAlertText(alert), fmt.Sprintf("%s on %s", alert.TransactionType, alert.Blockchain), alert.Transaction.Hash)
		case "socials":
			// Received a Whale Alert post to Twitter/Telegram/Other
			var socials SocialsJSON
			if err := json.Unmarshal(buf, &socials); err != nil {
				log.Println("error unmarshalling alert:", err)
				continue
			}

			w.postToWebhook(socials.Text, strings.Join(socials.URLS, ", "), socials.Blockchain)
		default:
			// Unknown message type
			log.Println("unknown message type:", msg.Type)
		}
	}
}

func (w *WebSocket) pause() {
	select {
	case <-w.quit:
	case <-time.NewTimer(time.Second * 10).C:
	}
}

// postToWebhook posts to the IFTTT webhook
func (w *WebSocket) postToWebhook(value1, value2, value3 string) {
	// IFTTT has two types of webhooks: "Receive a web request with a JSON payload" and "Receive a web request". This example
	// uses the second type. This only allows us to send only 3 values to IFTTT. To send more values a webhook of the first
	// type should be used in combination with "filter code" which is only available in the paid version of IFTTT. For more
	// info see: https://help.ifttt.com/hc/en-us/articles/4405029291163-Parsing-JSON-body-with-filter-code
	// First check if the rate limit has been reached
	if !w.iftttRateLimiter.Allow() {
		log.Println("IFTTT rate limit reached. Currently set to", w.iftttRateLimiter.Limit()*60, "per minute")
		return
	}

	type Payload struct {
		Value1 string `json:"value1,omitempty"`
		Value2 string `json:"value2,omitempty"`
		Value3 string `json:"value3,omitempty"`
	}

	payload, err := json.Marshal(&Payload{
		Value1: value1,
		Value2: value2,
		Value3: value3,
	})
	if err != nil {
		log.Println("error posting to webhook:", err)
		w.pause()
	}

	// Attempt to post the webhook to IFTTT. Retry up to 2 times
	for i := 0; i < 3; i++ {
		// Post webhook
		resp, err := http.Post(fmt.Sprintf(iftttWebhookURL, iftttWebhookEventName, iftttApiKey), "application/json", bytes.NewBuffer(payload))
		if err != nil {
			log.Println("error posting to webhook:", err)
			w.pause()
			continue
		}

		// Check the response status code
		if resp.StatusCode != http.StatusOK {
			log.Println("error posting to webhook:", resp.Status)
			w.pause()
			continue
		}

		log.Println("Posted to webhook", value1, value2, value3)
		break
	}
}

// Subscribe to alerts
func (w *WebSocket) subscribeAlerts() error {
	// Set a timeout context for the subscription
	subCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// Send the subscription message
	return wsjson.Write(subCtx, w.conn, &subscription)
}

// Connect to the WebSocket server
func (w *WebSocket) connect() error {
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()

	// Connect to the WebSocket server
	c, _, err := websocket.Dial(ctx, fmt.Sprintf("wss://leviathan.whale-alert.io/ws?api_key=%s", whaleAlertApiKey), nil)
	if err != nil {
		return err
	}

	w.conn = c

	log.Println("Connected to WebSocket server")

	return nil
}

func createAlertText(alert AlertJSON) string {
	const (
		amountText = "%s %s (%s USD)"

		transferText = "%s transferred from %s to %s"
		mintText     = "%s minted at %s"
		burnText     = "%s burned at %s"
		lockText     = "%s locked at %s"
		unlockText   = "%s unlocked at %s"
		freezeText   = "%s frozen at %s"
		unfreezeText = "%s unfrozen at %s"
	)

	// Build the amount string. In some cases the transaction can have multiple symbols and amounts
	var amountsSlice []string
	for _, a := range alert.Amounts {
		amountsSlice = append(amountsSlice, fmt.Sprintf(amountText, humanize.CommafWithDigits(a.Amount, 0), a.Symbol, humanize.CommafWithDigits(a.ValueUsd, 0)))
	}
	amounts := strings.Join(amountsSlice, ", ")

	switch alert.TransactionType {
	case "transfer":
		return fmt.Sprintf(transferText, amounts, alert.From, alert.To)
	case "mint":
		return fmt.Sprintf(mintText, amounts, alert.To)
	case "burn":
		return fmt.Sprintf(burnText, amounts, alert.From)
	case "lock":
		return fmt.Sprintf(lockText, amounts, alert.From)
	case "unlock":
		return fmt.Sprintf(unlockText, amounts, alert.To)
	case "freeze":
		return fmt.Sprintf(freezeText, amounts, alert.From)
	case "unfreeze":
		return fmt.Sprintf(unfreezeText, amounts, alert.To)
	default:
		return alert.Text
	}
}
