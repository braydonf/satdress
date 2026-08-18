package nwc

import (
	"context"
	"net/url"
	"net/http"
	"encoding/base64"
	"encoding/json"
	"io"
	"fmt"
	"strings"
	"sort"

	decodepay "github.com/nbd-wtf/ln-decodepay"
)

type PhoenixBalanceResult struct {
	BalanceSat uint64 `json:"balanceSat"`
	FeeCreditSat uint64 `json:"feeCreditSat"`
}

type PhoenixPayInvoiceResult struct {
	RecipientAmountSat uint64 `json:"recipientAmountSat"`
	RoutingFeeSat uint64 `json:"routingFeeSat"`
	PaymentId string `json:"paymentId"`
	PaymentPreimage string `json:"paymentPreimage"`
	PaymentHash string `json:"paymentHash"`
	UUID string `json:"uuid"`
}

type PhoenixLookupInvoiceResult struct {
	CompletedAt uint `json:"completedAt,omitempty"`
	CreatedAt uint `json:"createdAt"`
	Description string `json:"description"`
	DescriptionHash string `json:"descriptionHash"`
	ExternalId string `json:"externalId"`
	Fees uint64 `json:"fees,omitempty"`
	Invoice string `json:"invoice"`
	IsPaid bool `json:"isPaid"`
	PaymentHash string `json:"paymentHash"`
	Preimage string `json:"preimage,omitempty"`
	ReceivedSat uint64 `json:"receivedSat"`
}

type PhoenixTransactionResult struct {
	CompletedAt uint `json:"completedAt"`
	CreatedAt uint `json:"createdAt"`
	Description string `json:"description"`
	DescriptionHash string `json:"descriptionHash"`
	ExternalId string `json:"externalId"`
	Fees uint64 `json:"fees"`
	Invoice string `json:"invoice"`
	IsPaid bool `json:"isPaid"`
	Sent uint64 `json:"sent,omitempty"`
	PaymentHash string `json:"paymentHash"`
	Preimage string `json:"preimage"`
	ReceivedSat uint64 `json:"receivedSat"`
}

type PhoenixInvoiceResult struct {
	Amount uint64 `json:"amountSat"`
	PaymentHash string `json:"paymentHash"`
	Invoice string `json:"serialized"`
}

type PhoenixGetInfoResult struct {
	NodeId string `json:"nodeId"`
	// TODO channels
	BlockHeight uint `json:"blockHeight"`
	Chain string `json:"chain"`
	Version string `json:"version"`
	// TODO extensions
	// https://github.com/nostr-protocol/nips/blob/master/47.md#get_info
}

type Backend interface {
	HandlePayInvoice(context.Context, Nip47Request) (*Nip47Response, *Nip47Error)
	HandleGetBalance(context.Context, Nip47Request) (*Nip47Response, *Nip47Error)
	HandleMakeInvoice(context.Context, Nip47Request) (*Nip47Response, *Nip47Error)
	HandleLookupInvoice(context.Context, Nip47Request) (*Nip47Response, *Nip47Error)
	HandleListTransactions(context.Context, Nip47Request) (*Nip47Response, *Nip47Error)
	HandleGetInfo(context.Context, Nip47Request) (*Nip47Response, *Nip47Error)
	HandleUnknownMethod(context.Context, Nip47Request) (*Nip47Response, *Nip47Error)
}

type PhoenixBackend struct {
	Host string
	Key string
}

func (b *PhoenixBackend) payInvoice(invoice string) (*PhoenixPayInvoiceResult, error) {
	payload := url.Values{}
	payload.Set("invoice", invoice)

	client := &http.Client{}
	req, err := http.NewRequest(
		"POST",
		"http://"+b.Host+"/payinvoice",
		strings.NewReader(payload.Encode()),
	)

	if err != nil {
		return nil, err
	}

	keyb64 := base64.StdEncoding.EncodeToString([]byte("phoenix-cli:"+b.Key))

	req.Header.Add("Authorization", "Basic "+keyb64)
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	// TODO Paying to a hodl invoice (e.g. Zeuspay) will cause this
	// request to timeout, it could be good to have a better way to
	// handle this case.

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		text := string(body)
		if len(text) > 300 {
			text = text[:300]
		}
		return nil, fmt.Errorf("call to phoenix failed (%d): %s", res.StatusCode, text)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var result = PhoenixPayInvoiceResult{}

	err = json.Unmarshal([]byte(body), &result)

	if err != nil {
		return nil, err
	}

	return &result, nil
}

func (b *PhoenixBackend) getBalance() (uint64, error) {
	client := &http.Client{}
	req, err := http.NewRequest(
		"GET",
		"http://"+b.Host+"/getbalance",
		nil,
	)

	if err != nil {
		return 0, err
	}

	keyb64 := base64.StdEncoding.EncodeToString([]byte("phoenix-cli:"+b.Key))

	req.Header.Add("Authorization", "Basic "+keyb64)

	res, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		text := string(body)
		if len(text) > 300 {
			text = text[:300]
		}
		return 0, fmt.Errorf("call to phoenix failed (%d): %s", res.StatusCode, text)
		
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, err
	}

	var result = PhoenixBalanceResult{}

	err = json.Unmarshal([]byte(body), &result)

	if err != nil {
		return 0, err
	}

	return result.BalanceSat, nil
}

func (b *PhoenixBackend) lookupInvoice(paymentHash string) (*PhoenixLookupInvoiceResult, error) {
	result, err := b.lookupIncomingInvoice(paymentHash)
	if result == nil && err == nil {
		return b.lookupOutgoingInvoice(paymentHash)
	} else if err != nil {
		return nil, err
	}

	return result, nil
}

func (b *PhoenixBackend) lookupIncomingInvoice(paymentHash string) (*PhoenixLookupInvoiceResult, error) {
	client := &http.Client{}
	req, err := http.NewRequest(
		"GET",
		"http://"+b.Host+"/payments/incoming/"+paymentHash,
		nil,
	)

	if err != nil {
		return nil, err
	}

	keyb64 := base64.StdEncoding.EncodeToString([]byte("phoenix-cli:"+b.Key))

	req.Header.Add("Authorization", "Basic "+keyb64)

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode == 404 {
		return nil, nil
	} else if res.StatusCode >= 400 {
		body, _ := io.ReadAll(res.Body)
		text := string(body)
		if len(text) > 300 {
			text = text[:300]
		}
		return nil, fmt.Errorf("call to phoenix failed (%d): %s", res.StatusCode, text)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var result = PhoenixLookupInvoiceResult{}

	err = json.Unmarshal([]byte(body), &result)

	if err != nil {
		return nil, err
	}

	return &result, nil
}

func (b *PhoenixBackend) lookupOutgoingInvoice(paymentHash string) (*PhoenixLookupInvoiceResult, error) {
	client := &http.Client{}
	req, err := http.NewRequest(
		"GET",
		"http://"+b.Host+"/payments/outgoingbyhash/"+paymentHash,
		nil,
	)

	if err != nil {
		return nil, err
	}

	keyb64 := base64.StdEncoding.EncodeToString([]byte("phoenix-cli:"+b.Key))

	req.Header.Add("Authorization", "Basic "+keyb64)

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode == 404 {
		return nil, nil
	} else if res.StatusCode >= 400 {
		body, _ := io.ReadAll(res.Body)
		text := string(body)
		if len(text) > 300 {
			text = text[:300]
		}
		return nil, fmt.Errorf("call to phoenix failed (%d): %s", res.StatusCode, text)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var result = PhoenixLookupInvoiceResult{}
	err = json.Unmarshal([]byte(body), &result)
	if err != nil {
		return nil, err
	}

	return &result, nil
}

func (b *PhoenixBackend) makeInvoice(params Nip47InvoiceParams) (*PhoenixInvoiceResult, error) {
	payload := url.Values{}

	if params.DescriptionHash != "" {
		payload.Set("descriptionHash", params.DescriptionHash)
	} else {
		payload.Set("description", params.Description)
	}

	payload.Add("expirySeconds", fmt.Sprintf("%d", params.Expiry))

	payload.Add("amountSat", fmt.Sprintf("%d", params.Amount/1000))

	client := &http.Client{}
	req, err := http.NewRequest(
		"POST",
		"http://"+b.Host+"/createinvoice",
		strings.NewReader(payload.Encode()),
	)

	if err != nil {
		return nil, err
	}

	keyb64 := base64.StdEncoding.EncodeToString([]byte("phoenix-cli:"+b.Key))

	req.Header.Add("Authorization", "Basic "+keyb64)
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		text := string(body)
		if len(text) > 300 {
			text = text[:300]
		}
		return nil, fmt.Errorf("call to phoenix failed (%d): %s", res.StatusCode, text)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var result = PhoenixInvoiceResult{}

	err = json.Unmarshal([]byte(body), &result)

	if err != nil {
		return nil, err
	}

	return &result, nil
}

func (b *PhoenixBackend) getInfo() (*PhoenixGetInfoResult, error) {
	client := &http.Client{}
	req, err := http.NewRequest(
		"GET",
		"http://"+b.Host+"/getinfo",
		nil,
	)

	if err != nil {
		return nil, err
	}

	keyb64 := base64.StdEncoding.EncodeToString([]byte("phoenix-cli:"+b.Key))

	req.Header.Add("Authorization", "Basic "+keyb64)

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		text := string(body)
		if len(text) > 300 {
			text = text[:300]
		}
		return nil, fmt.Errorf("call to phoenix failed (%d): %s", res.StatusCode, text)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var result = PhoenixGetInfoResult{}

	err = json.Unmarshal([]byte(body), &result)

	if err != nil {
		return nil, err
	}

	return &result, nil
}

func (b *PhoenixBackend) listTransactions(params Nip47ListTransactionsParams) (*[]PhoenixTransactionResult, error) {
	payload := url.Values{}
	// unused parameters: offset, externalId
	if params.From > 0 {
		payload.Add("from", fmt.Sprintf("%d", params.From * 1000)) // milliseconds
	}

	if params.Until > 0 {
		payload.Add("to", fmt.Sprintf("%d", params.Until * 1000)) // milliseconds
	}

	if params.Limit > 0 {
		payload.Add("limit", fmt.Sprintf("%d", params.Limit))
	}

	if params.Unpaid {
		payload.Add("all", "true")
	} else {
		payload.Add("all", "false")
	}

	client := &http.Client{}
	req, err := http.NewRequest(
		"GET",
		"http://"+b.Host+"/payments/incoming?"+payload.Encode(),
		nil,
	)

	if err != nil {
		return nil, err
	}

	keyb64 := base64.StdEncoding.EncodeToString([]byte("phoenix-cli:"+b.Key))

	req.Header.Add("Authorization", "Basic "+keyb64)

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		text := string(body)
		if len(text) > 300 {
			text = text[:300]
		}
		return nil, fmt.Errorf("call to phoenix failed (%d): %s", res.StatusCode, text)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var result = []PhoenixTransactionResult{}

	err = json.Unmarshal([]byte(body), &result)

	if err != nil {
		return nil, err
	}

	return &result, nil

}

func (b *PhoenixBackend) listPayments(params Nip47ListTransactionsParams) (*[]PhoenixTransactionResult, error) {
	payload := url.Values{}
	// unused parameters: offset
	if params.From > 0 {
		payload.Add("from", fmt.Sprintf("%d", params.From * 1000)) // milliseconds
	}

	if params.Until > 0 {
		payload.Add("to", fmt.Sprintf("%d", params.Until * 1000)) // milliseconds
	}

	if params.Limit > 0 {
		payload.Add("limit", fmt.Sprintf("%d", params.Limit))
	}

	if params.Unpaid {
		payload.Add("all", "true")
	} else {
		payload.Add("all", "false")
	}

	client := &http.Client{}
	req, err := http.NewRequest(
		"GET",
		"http://"+b.Host+"/payments/outgoing?"+payload.Encode(),
		nil,
	)

	if err != nil {
		return nil, err
	}

	keyb64 := base64.StdEncoding.EncodeToString([]byte("phoenix-cli:"+b.Key))

	req.Header.Add("Authorization", "Basic "+keyb64)

	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		body, _ := io.ReadAll(res.Body)
		text := string(body)
		if len(text) > 300 {
			text = text[:300]
		}
		return nil, fmt.Errorf("call to phoenix failed (%d): %s", res.StatusCode, text)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var result = []PhoenixTransactionResult{}

	err = json.Unmarshal([]byte(body), &result)

	if err != nil {
		return nil, err
	}

	return &result, nil

}

func (b *PhoenixBackend) HandleGetBalance(ctx context.Context, nip47req Nip47Request) (*Nip47Response, *Nip47Error) {

	sats, err := b.getBalance()

	if err != nil {
		return nil, &Nip47Error{
			Code: NIP47_ERROR_INTERNAL,
			Message: "could not get balance",
		}
	}

	msats := sats * 1000

	response := Nip47Response{
		ResultType: "get_balance",
		Result: Nip47GetBalanceResult{
			Balance: msats,
		},
	}

	return &response, nil
}

func (b *PhoenixBackend) HandleLookupInvoice(ctx context.Context, nip47req Nip47Request) (*Nip47Response, *Nip47Error) {


	var params Nip47LookupInvoiceParams

	err := json.Unmarshal(nip47req.Params, &params)

	var paymentHash string

	if params.PaymentHash == "" {
		paymentRequest, err := decodepay.Decodepay(strings.ToLower(params.Invoice))
		if err != nil {
			return nil, &Nip47Error{
				Code: NIP47_ERROR_INTERNAL,
				Message: "could not decode invoice",
			}
		}
		paymentHash = paymentRequest.PaymentHash
	} else {
		paymentHash = params.PaymentHash
	}

	result, err := b.lookupInvoice(paymentHash)

	if result == nil {
		return nil, &Nip47Error{
			Code: NIP47_ERROR_NOT_FOUND,
			Message: "could not find invoice",
		}
	}

	if err != nil {
		return nil, &Nip47Error{
			Code: NIP47_ERROR_INTERNAL,
			Message: "could not load invoice",
		}
	}

	bolt11, err := decodepay.Decodepay(result.Invoice)

	if err != nil {
		return nil, &Nip47Error{
			Code: NIP47_ERROR_INTERNAL,
			Message: "could not decode invoice",
		}
	}

	var expiresAt uint
	if bolt11.Expiry > 0 {
		expiresAt = result.CreatedAt + (uint(bolt11.Expiry) * 1000)
	} else {
		expiresAt = 0
	}

	nip47result := Nip47InvoiceResult{
		Type: "incoming",
		Invoice: result.Invoice,
		Description: result.Description,
		DescriptionHash: bolt11.DescriptionHash,
		Preimage: result.Preimage,
		PaymentHash: paymentHash,
		Amount: uint64(bolt11.MSatoshi),
		FeesPaid: result.Fees,
		CreatedAt: result.CreatedAt / 1000, // seconds
		// TODO metadata
	}

	if result.IsPaid {
		nip47result.SettledAt = result.CreatedAt / 1000 // seconds
	}

	if expiresAt > 0 {
		nip47result.ExpiresAt = expiresAt / 1000
	}

	response := Nip47Response{
		ResultType: "lookup_invoice",
		Result: nip47result,
	}

	return &response, nil
}

func (b *PhoenixBackend) HandleListTransactions(ctx context.Context, nip47req Nip47Request) (*Nip47Response, *Nip47Error) {
	// TODO Full transaction history
	// https://github.com/nostr-wallet-connect/nwc/blob/main/05.md
	// https://github.com/ACINQ/phoenixd/blob/master/src/commonMain/kotlin/fr/acinq/phoenixd/Api.kt
	// - Sync new incoming and outgoing payments to NWC database
	// - Query NWC database to support combining both incoming
	//   and outgoing w/ offset, from, to, limit

	var params Nip47ListTransactionsParams

	err := json.Unmarshal(nip47req.Params, &params)

	result, err := b.listTransactions(params)

	if err != nil {
		return nil, &Nip47Error{
			Code: NIP47_ERROR_INTERNAL,
			Message: fmt.Sprintf("could not list transactions (%s)", err),
		}
	}

	payments, err := b.listPayments(params)

	if err != nil {
		return nil, &Nip47Error{
			Code: NIP47_ERROR_INTERNAL,
			Message: fmt.Sprintf("could not list payments (%s)", err),
		}
	}

	txs := make([]Nip47InvoiceResult, len(*result) + len(*payments))
	var offset int

	for i, tx := range *result {
		bolt11, err := decodepay.Decodepay(tx.Invoice)

		if err != nil {
			return nil, &Nip47Error{
				Code: NIP47_ERROR_INTERNAL,
				Message: "could not decode invoice",
			}
		}

		var expiresAt uint
		if bolt11.Expiry > 0 {
			expiresAt = tx.CreatedAt + (uint(bolt11.Expiry) * 1000)
		} else {
			expiresAt = 0
		}

		txs[i].Type = "incoming"

		txs[i].PaymentHash = tx.PaymentHash

		if tx.IsPaid {
			txs[i].Preimage = tx.Preimage
		}

		txs[i].Description = tx.Description
		txs[i].DescriptionHash = bolt11.DescriptionHash
		txs[i].Invoice = tx.Invoice
		txs[i].FeesPaid = tx.Fees * 1000 // msats
		txs[i].Amount = tx.ReceivedSat * 1000 // msats
		txs[i].CreatedAt = tx.CreatedAt / 1000 // seconds

		if tx.IsPaid {
			txs[i].SettledAt = tx.CompletedAt / 1000 // seconds
		}

		if expiresAt > 0 {
			txs[i].ExpiresAt = expiresAt / 1000 // seconds
		}

		offset += 1
	}

	for i, payment := range *payments {
		bolt11, err := decodepay.Decodepay(payment.Invoice)

		if err != nil {
			return nil, &Nip47Error{
				Code: NIP47_ERROR_INTERNAL,
				Message: "could not decode invoice",
			}
		}

		var expiresAt uint
		if bolt11.Expiry > 0 {
			expiresAt = payment.CreatedAt + (uint(bolt11.Expiry) * 1000)
		} else {
			expiresAt = 0
		}

		txs[offset + i].Type = "outgoing"

		txs[offset + i].PaymentHash = payment.PaymentHash

		if payment.IsPaid {
			txs[offset + i].Preimage = payment.Preimage
		}

		txs[offset + i].Description = payment.Description
		txs[offset + i].DescriptionHash = bolt11.DescriptionHash
		txs[offset + i].Invoice = payment.Invoice
		txs[offset + i].FeesPaid = payment.Fees * 1000 // msats
		txs[offset + i].Amount = payment.Sent * 1000 // msats
		txs[offset + i].CreatedAt = payment.CreatedAt / 1000 // seconds

		if payment.IsPaid {
			txs[offset + i].SettledAt = payment.CompletedAt / 1000 // seconds
		}

		if expiresAt > 0 {
			txs[offset + i].ExpiresAt = expiresAt / 1000 // seconds
		}
	}

	sort.SliceStable(txs, func(i, j int) bool {
		return txs[i].CreatedAt > txs[j].CreatedAt
	})

	max := len(txs) - 1
	if max + 1 > int(params.Limit) {
		max = int(params.Limit) - 1
	}

	response := &Nip47Response{
		ResultType: "list_transactions",
		Result: Nip47ListTransactionsResult{
			Transactions: txs[:max],
		},
	}

	return response, nil
}

func (b *PhoenixBackend) HandleMakeInvoice(ctx context.Context, nip47req Nip47Request) (*Nip47Response, *Nip47Error) {

	var params Nip47InvoiceParams

	err := json.Unmarshal(nip47req.Params, &params)

	result, err := b.makeInvoice(params)

	if err != nil {
		return nil, &Nip47Error{
			Code: NIP47_ERROR_INTERNAL,
			Message: "could not create invoice",
		}
	}

	bolt11, err := decodepay.Decodepay(result.Invoice)

	if err != nil {
		return nil, &Nip47Error{
			Code: NIP47_ERROR_INTERNAL,
			Message: "could not decode invoice",
		}
	}

	var expiresAt uint
	if bolt11.Expiry > 0 {
		expiresAt = uint(bolt11.CreatedAt) + (uint(bolt11.Expiry) * 1000)
	} else {
		expiresAt = 0
	}

	nip47result := Nip47InvoiceResult{
		Type: "incoming",
		Invoice: result.Invoice,
		Description: bolt11.Description,
		DescriptionHash: bolt11.DescriptionHash,
		PaymentHash: bolt11.PaymentHash,
		Amount: uint64(bolt11.MSatoshi),
		CreatedAt: uint(bolt11.CreatedAt),
	}

	if expiresAt > 0 {
		nip47result.ExpiresAt = expiresAt
	}

	response := &Nip47Response{
		ResultType: "make_invoice",
		Result: nip47result,
	}

	return response, nil
}

func (b *PhoenixBackend) HandleGetInfo(ctx context.Context, nip47req Nip47Request) (*Nip47Response, *Nip47Error) {
	result, err := b.getInfo()

	if err != nil {
		return nil, &Nip47Error{
			Code: NIP47_ERROR_INTERNAL,
			Message: "could not get information",
		}
	}

	response := &Nip47Response{
		ResultType: "get_info",
		Result: Nip47GetInfoResult{
			//Alias: nil,
			//Color: nil,
			//PubKey: nil,
			Network: result.Chain, // mainnet, testnet, signet, or regtest
			BlockHeight: result.BlockHeight,
			//BlockHash: nil,
			Methods: strings.Split(NIP47_CAPABILITIES, " "),
		},
	}

	return response, nil
}

func (b *PhoenixBackend) HandlePayInvoice(ctx context.Context, nip47req Nip47Request) (*Nip47Response, *Nip47Error) {

	var params Nip47PayParams

	err := json.Unmarshal(nip47req.Params, &params)

	if err != nil {
		return nil, &Nip47Error{
			Code: NIP47_ERROR_INTERNAL,
			Message: "could not decode",
		}
	}

	result, err := b.payInvoice(params.Invoice)

	if err != nil {
		return nil, &Nip47Error{
			Code: NIP47_ERROR_INTERNAL,
			Message: "could not pay",
		}
	}

	response := Nip47Response{
		ResultType: "pay_invoice",
		Result: Nip47PayInvoiceResult{
			Preimage: result.PaymentPreimage,
		},
	}

	return &response, nil
}

func (b *PhoenixBackend) HandleUnknownMethod(ctx context.Context, nip47req Nip47Request) (*Nip47Response, *Nip47Error) {

	return &Nip47Response{}, &Nip47Error{}
}
