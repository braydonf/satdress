package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/fiatjaf/go-lnurl"
	"github.com/gorilla/mux"
	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	decodepay "github.com/nbd-wtf/ln-decodepay"
)

var allowNostr bool = false
var nostrPrivkeyHex string = ""
var nostrPubkey string = ""
var minSendable uint64 = 1000
var maxSendable uint64 = 1000000000
var CommentAllowed int = 2000

type LNURLPayParamsCustom struct {
	lnurl.LNURLResponse
	Callback        string               `json:"callback"`
	Tag             string               `json:"tag"`
	MaxSendable     int64                `json:"maxSendable"`
	MinSendable     int64                `json:"minSendable"`
	EncodedMetadata string               `json:"metadata"`
	CommentAllowed  int64                `json:"commentAllowed"`
	PayerData       *lnurl.PayerDataSpec `json:"payerData,omitempty"`
	AllowsNostr     bool                 `json:"allowsNostr,omitempty"`
	NostrPubKey     string               `json:"nostrPubkey,omitempty"`
	Metadata        lnurl.Metadata       `json:"-"`
}

type LNURLPayValuesCustom struct {
	lnurl.LNURLResponse
	SuccessAction      *lnurl.SuccessAction `json:"successAction"`
	Routes             interface{}          `json:"routes"` // ignored
	PR                 string               `json:"pr"`
	Disposable         *bool                `json:"disposable,omitempty"`
	Comment            string               `json:"comment"`
	CreatedAt          time.Time            `json:"created_at"`
	Paid               bool                 `json:"paid"`
	PaidAt             time.Time            `json:"paid_at"`
	From               string               `json:"from"`
	ParsedInvoice      decodepay.Bolt11     `json:"-"`
	PayerDataJSON      string               `json:"-"`
	Nip57Receipt       nostr.Event          `json:"nip57Receipt"`
	Nip57ReceiptRelays []string             `json:"nip57ReceiptRelays"`
	AwaitInvoicePaid   bool                 `json:"awaitInvoicePaid"`
	Sender             string               `json:"sender"`
	Note               string               `json:"note"`
}

func handleLNURL(w http.ResponseWriter, r *http.Request) {
	var response interface{}

	username := mux.Vars(r)["user"]
	domain := s.Domain

	params := getParams(username)
	if params == nil {
		log.Debug().Str("name", username).Str("domain", domain).Msg("failed to get name")
		json.NewEncoder(w).Encode(lnurl.ErrorResponse(fmt.Sprintf(
			"failed to get name %s@%s", username, domain)))
		return
	}

	log.Info().Str("username", username).Str("domain", domain).Msg("got lnurl request")

	if amount := r.URL.Query().Get("amount"); amount == "" {
		// check if the receiver accepts comments
		var commentLength int64 = 0
		// TODO: support webhook comments

		// convert configured sendable amounts to integer
		minSendable, err := strconv.ParseInt(params.MinSendable, 10, 64)
		// set defaults
		if err != nil {
			minSendable = 1000
		}
		maxSendable, err := strconv.ParseInt(params.MaxSendable, 10, 64)
		if err != nil {
			maxSendable = 1000000000
		}

		// if a nostr private nsec key is set, set nostr nip57 flags
		if len(s.NostrPrivateKey) > 0 {
			//allows users to use nsec keys, work with hex internally.
			//This can be any private key, not necessarily from the user.
			nostrPrivkeyHex = DecodeBech32(s.NostrPrivateKey)
			allowNostr = true
			privkey, err := nostr.SecretKeyFromHex(nostrPrivkeyHex)
			if err != nil {
				log.Error().Err(err).Str("Couldn't parse privkey: ", err.Error())
			} else {
				pub := nostr.GetPublicKey(privkey)
				nostrPubkey = pub.Hex()
			}
		}

		json.NewEncoder(w).Encode(LNURLPayParamsCustom{
			LNURLResponse:   lnurl.LNURLResponse{Status: "OK"},
			Callback:        fmt.Sprintf("https://%s/.well-known/lnurlp/%s", domain, username),
			MinSendable:     minSendable,
			MaxSendable:     maxSendable,
			EncodedMetadata: makeMetadata(params),
			CommentAllowed:  commentLength,
			Tag:             "payRequest",
			AllowsNostr:     allowNostr,
			NostrPubKey:     nostrPubkey,
		})

	} else {
		msat, err := strconv.ParseUint(amount, 10, 64)
		if err != nil {
			json.NewEncoder(w).Encode(lnurl.ErrorResponse("amount is not integer"))
			return
		}

		var comment = ""
		var payerData lnurl.PayerDataValues
		// nostr NIP-57
		// the "nostr" query param has a zap request which is a nostr event
		// that specifies which nostr note has been zapped.
		// here we check wheter its present, the event signature is valid
		// and whether the event has the necessary tags that we need (p and relays are necessary, e is optional)

		zapEventQuery := r.FormValue("nostr")
		var zapEvent nostr.Event
		if len(zapEventQuery) > 0 {
			err = json.Unmarshal([]byte(zapEventQuery), &zapEvent)
			if err != nil {
				log.Error().Err(err).Str("Couldn't parse nostr event: ", err.Error())
			} else {
				valid := zapEvent.VerifySignature()
				if !valid {
					log.Warn().Msg("Nostr NIP-57 zap event signature invalid")
					return
				}
				if !zapEvent.Tags.Has("p") {
					log.Error().Err(err).Str("Nostr NIP-57 zap event validation error ", "")
					return
				}
			}
			if len(zapEvent.Content) > 0 {
				comment = zapEvent.Content
				log.Debug().Str("NIP57 Comment received", comment).Msg("Comment")
			}

		}
		//We can't handle comments and payerdata in NIP57 at the same time...

		// If a comment is send with the Invoice, always use it (?)
		regularcomment := r.FormValue("comment")
		if len(regularcomment) > CommentAllowed {
			log.Error().Err(err).Str("Comment is too long", err.Error())
			return
		}
		if len(regularcomment) > 0 {
			comment = regularcomment
			log.Debug().Str("Comment received", comment).Msg("Comment")
		}
		// payer data, not used currently
		payerdata := r.FormValue("payerdata")

		if len(payerdata) > 0 {
			err = json.Unmarshal([]byte(payerdata), &payerData)
			if err != nil {
				log.Error().Err(err).Str("Couldn't parse payerdata", err.Error())
			}
		}

		//we outsource the second part in a function, we should do this for the first one too.
		response, err = serveLNURLpSecond(w, params, username, msat, comment, payerData, zapEvent)
		var payvaluescustom = response.(LNURLPayValuesCustom)
		if err != nil {
			// there is a valid error response
			json.NewEncoder(w).Encode(response)
			return
		}

		json.NewEncoder(w).Encode(lnurl.LNURLPayValues{
			LNURLResponse: payvaluescustom.LNURLResponse,
			PR:            payvaluescustom.PR,
			Routes:        payvaluescustom.Routes,
			SuccessAction: payvaluescustom.SuccessAction,
		})

		//if we provided a nsec and the response contained zap information, we wait for the invoice to be paid
		//in order to submit the zap on nostr
		//also check for invoice paid for regular ln payments for nostr notificaitons
		if payvaluescustom.AwaitInvoicePaid {
			go WaitForInvoicePaid(payvaluescustom, params)
		}
	}
}

func serveLNURLpSecond(w http.ResponseWriter, params *UserParams, username string, amount_msat uint64, comment string, payerData lnurl.PayerDataValues, zapEvent nostr.Event) (LNURLPayValuesCustom, error) {
	log.Debug().Any("Serving invoice for user %s", username)
	if amount_msat < minSendable || amount_msat > maxSendable {
		// amount is not ok
		return LNURLPayValuesCustom{
			LNURLResponse: lnurl.LNURLResponse{
				Status: "Error",
				Reason: fmt.Sprintf("Amount out of bounds (min: %d sat, max: %d sat).", minSendable/1000, maxSendable/1000)},
		}, fmt.Errorf("amount out of bounds")
	}

	// NIP57 ZAPs
	// for nip57 use the nostr event as the descriptionHash
	if len(zapEvent.Sig) > 0 {

		// we calculate the descriptionHash here, create an invoice with it
		// and store the invoice in the zap receipt later down the line
		zapEventSerialized, err := json.Marshal(zapEvent)
		zapEventSerializedStr = fmt.Sprintf("%s", zapEventSerialized)
		if err != nil {
			return LNURLPayValuesCustom{
				LNURLResponse: lnurl.LNURLResponse{
					Status: "Error",
					Reason: "Couldn't serialize zap event."},
			}, err
		}
		// we extract the relays from the zap request
		nip57ReceiptRelays = ExtractNostrRelays(zapEvent)

	} else {
		//If we have a regular call, we ignore zapEvent in makeinvoice later.
		zapEventSerializedStr = ""
		log.Debug().Str("Regular Invoice", "Not an NIP57 event").Msg("Note")
	}

	var response LNURLPayValuesCustom
	invoice, err := makeInvoice(params, amount_msat, zapEventSerializedStr, comment)
	if err != nil {
		err = fmt.Errorf("couldn't create invoice: %v", err.Error())
		response = LNURLPayValuesCustom{
			LNURLResponse: lnurl.LNURLResponse{
				Status: "Error",
				Reason: "Couldn't create invoice."},
		}
		return response, err
	}

	//Check invoice paid only if we actually have a NIP57 event
	var awaitPaid = true
	var sender = ""
	var note = ""
	// nip57 - we need to store the newly created invoice in the zap receipt
	if len(zapEvent.Sig) > 0 {
		nip57Receipt, err = CreateNostrReceipt(zapEvent, invoice)
		if err != nil {
			response = LNURLPayValuesCustom{
				LNURLResponse: lnurl.LNURLResponse{
					Status: "Error",
					Reason: "Couldn't create receipt."},
			}
			return response, err
		}
		sender = "@" + nip19.EncodeNpub(zapEvent.PubKey)
		etag := zapEvent.Tags.Find("e")
		if etag != nil {
			notebech32, err := EncodeBech32Note(etag[1])
			if err != nil {
				note = etag[1]
			} else {
				note = "@" + notebech32
			}
		}
		if zapEvent.Tags.Find("e") == nil {
			sender = "anonymous Zapper 🤙"
		}
		log.Debug().Str("Zap from", sender).Msg("Nostr")
	}

	decoded_invoice, _ := decodepay.Decodepay(invoice)
	return LNURLPayValuesCustom{
		LNURLResponse:      lnurl.LNURLResponse{Status: "OK"},
		PR:                 invoice,
		Routes:             make([]struct{}, 0),
		SuccessAction:      &lnurl.SuccessAction{Message: "Payment Received!", Tag: "message"},
		Comment:            comment,
		Paid:               false,
		CreatedAt:          time.Now(),
		ParsedInvoice:      decoded_invoice,
		Nip57Receipt:       nip57Receipt,
		Nip57ReceiptRelays: nip57ReceiptRelays,
		AwaitInvoicePaid:   awaitPaid,
		Sender:             sender,
		Note:               note,
	}, nil

}
