package makeinvoice

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/fiatjaf/eclair-go"
	lightning "github.com/fiatjaf/lightningd-gjson-rpc"
	lnsocket "github.com/jb55/lnsocket/go"
	"github.com/lnpay/lnpay-go"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	TorProxyURL = "socks5://127.0.0.1:9050"
	Client      = &http.Client{
		Timeout: 10 * time.Second,
	}
)

type LNParams struct {
	Backend     LNBackendParams
	Msatoshi    int64
	Description string

	// setting this to true will cause .Description to be hashed and used as
	// the description_hash (h) field on the bolt11 invoice
	UseDescriptionHash bool

	Label string // only used for c-lightning
}

type CommandoParams struct {
	Rune   string
	Host   string
	NodeId string
}

func (l CommandoParams) GetCert() string { return "" }
func (l CommandoParams) IsTor() bool     { return strings.Index(l.Host, ".onion") != -1 }

type SparkoParams struct {
	Cert string
	Host string
	Key  string
}

func (l SparkoParams) GetCert() string { return l.Cert }
func (l SparkoParams) IsTor() bool     { return strings.Index(l.Host, ".onion") != -1 }

type LNDParams struct {
	Cert     string
	Host     string
	Macaroon string
	Private  bool
}

func (l LNDParams) GetCert() string { return l.Cert }
func (l LNDParams) IsTor() bool     { return strings.Index(l.Host, ".onion") != -1 }

type LNBitsParams struct {
	Cert string
	Host string
	Key  string
}

func (l LNBitsParams) GetCert() string { return l.Cert }
func (l LNBitsParams) IsTor() bool     { return strings.Index(l.Host, ".onion") != -1 }

type LNPayParams struct {
	PublicAccessKey  string
	WalletInvoiceKey string
}

func (l LNPayParams) GetCert() string { return "" }
func (l LNPayParams) IsTor() bool     { return false }

type EclairParams struct {
	Host     string
	Password string
	Cert     string
}

func (l EclairParams) GetCert() string { return l.Cert }
func (l EclairParams) IsTor() bool     { return strings.Index(l.Host, ".onion") != -1 }

type StrikeParams struct {
	Key      string
	Username string
	Currency string
}

func (l StrikeParams) GetCert() string { return "" }
func (l StrikeParams) IsTor() bool     { return false }

type PhoenixParams struct {
       Host     string
       Key      string
}

func (l PhoenixParams) GetCert() string { return "" }
func (l PhoenixParams) IsTor() bool     { return false }

type LNBackendParams interface {
	GetCert() string
	IsTor() bool
}

func MakeInvoice(params LNParams) (bolt11 string, err error) {
	defer func(prevTransport http.RoundTripper) {
		Client.Transport = prevTransport
	}(Client.Transport)

	specialTransport := &http.Transport{}

	// use a cert or skip TLS verification?
	if params.Backend.GetCert() != "" {
		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM([]byte(params.Backend.GetCert()))
		specialTransport.TLSClientConfig = &tls.Config{RootCAs: caCertPool}
	} else {
		specialTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	// use a tor proxy?
	if params.Backend.IsTor() {
		torURL, _ := url.Parse(TorProxyURL)
		specialTransport.Proxy = http.ProxyURL(torURL)
	}

	Client.Transport = specialTransport

	// description hash?
	var hexh, b64h string
	if params.UseDescriptionHash {
		descriptionHash := sha256.Sum256([]byte(params.Description))
		hexh = hex.EncodeToString(descriptionHash[:])
		b64h = base64.StdEncoding.EncodeToString(descriptionHash[:])
	}

	switch backend := params.Backend.(type) {
	case SparkoParams:
		spark := &lightning.Client{
			SparkURL:    backend.Host,
			SparkToken:  backend.Key,
			CallTimeout: time.Second * 3,
		}

		var method, desc string
		if params.UseDescriptionHash {
			method = "invoicewithdescriptionhash"
			desc = hexh
		} else {
			method = "invoice"
			desc = params.Description
		}

		label := params.Label
		if label == "" {
			label = makeRandomLabel()
		}

		inv, err := spark.Call(method, params.Msatoshi, label, desc)
		if err != nil {
			return "", fmt.Errorf(method+" call failed: %w", err)
		}
		return inv.Get("bolt11").String(), nil

	case LNDParams:
		body, _ := sjson.Set("{}", "value_msat", params.Msatoshi)
		if params.UseDescriptionHash {
			body, _ = sjson.Set(body, "description_hash", b64h)
		} else {
			body, _ = sjson.Set(body, "memo", params.Description)
		}
		if backend.Private {
			body, _ = sjson.Set(body, "private", true)
		}

		req, err := http.NewRequest("POST",
			backend.Host+"/v1/invoices",
			bytes.NewBufferString(body),
		)
		if err != nil {
			return "", err
		}

		// macaroon must be hex, so if it is on base64 we adjust that
		if b, err := base64.StdEncoding.DecodeString(backend.Macaroon); err == nil {
			backend.Macaroon = hex.EncodeToString(b)
		}

		req.Header.Set("Grpc-Metadata-macaroon", backend.Macaroon)
		resp, err := Client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			text := string(body)
			if len(text) > 300 {
				text = text[:300]
			}
			return "", fmt.Errorf("call to lnd failed (%d): %s", resp.StatusCode, text)
		}

		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}

		return gjson.ParseBytes(b).Get("payment_request").String(), nil

	case LNBitsParams:
		body, _ := sjson.Set("{}", "amount", params.Msatoshi/1000)
		body, _ = sjson.Set(body, "out", false)

		if params.UseDescriptionHash {
			body, _ = sjson.Set(body, "unhashed_description", hex.EncodeToString([]byte(params.Description)))
		} else {
			if params.Description == "" {
				body, _ = sjson.Set(body, "memo", "created by makeinvoice")
			} else {
				body, _ = sjson.Set(body, "memo", params.Description)
			}
		}

		req, err := http.NewRequest("POST",
			backend.Host+"/api/v1/payments",
			bytes.NewBufferString(body),
		)
		if err != nil {
			return "", err
		}

		req.Header.Set("X-Api-Key", backend.Key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := Client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			text := string(body)
			if len(text) > 300 {
				text = text[:300]
			}
			return "", fmt.Errorf("call to lnbits failed (%d): %s", resp.StatusCode, text)
		}

		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}

		return gjson.ParseBytes(b).Get("payment_request").String(), nil

	case LNPayParams:
		client := lnpay.NewClient(backend.PublicAccessKey)
		wallet := client.Wallet(backend.WalletInvoiceKey)
		lntx, err := wallet.Invoice(lnpay.InvoiceParams{
			NumSatoshis:     params.Msatoshi / 1000,
			Memo:            params.Description,
			DescriptionHash: hexh,
		})
		if err != nil {
			return "", fmt.Errorf("error creating invoice on lnpay: %w", err)
		}

		return lntx.PaymentRequest, nil

	case EclairParams:
		client := eclair.Client{Host: backend.Host, Password: backend.Password}
		eclairParams := eclair.Params{"amountMsat": params.Msatoshi}

		if params.UseDescriptionHash {
			eclairParams["descriptionHash"] = hexh
		} else {
			eclairParams["description"] = params.Description
		}

		inv, err := client.Call("createinvoice", eclairParams)
		if err != nil {
			return "", fmt.Errorf("error creating invoice on eclair: %w", err)
		}

		return inv.Get("serialized").String(), nil

	case StrikeParams:
		payload := struct {
			Description string `json:"description"`
			Amount      struct {
				Currency string `json:"currency"`
				Amount   string `json:"amount"`
			} `json:"amount"`
		}{}

		payload.Description = "created by makeinvoice"
		if params.Description != "" {
			payload.Description = params.Description
		}

		// TODO: BTC currency does not seem to be supported at the moment Currently the currency needs to be the user's base currency (USD for the US, USDT for El Sal and Argentina). However, we're going to enable BTC invoices in the coming weeks.
		payload.Amount.Currency = backend.Currency
		payload.Amount.Amount = fmt.Sprintf("%.8f",
			float32(params.Msatoshi)/100000000000)

		jpayload := &bytes.Buffer{}
		json.NewEncoder(jpayload).Encode(payload)

		client := &http.Client{}
		req, err := http.NewRequest("POST",
			"https://api.strike.me/v1/invoices/handle/"+backend.Username, jpayload)
		if err != nil {
			return "", err
		}
		req.Header.Add("Content-Type", "application/json")
		req.Header.Add("Accept", "application/json")
		req.Header.Add("Authorization", "Bearer "+backend.Key)

		res, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer res.Body.Close()

		body, err := io.ReadAll(res.Body)
		if err != nil {
			return "", err
		}

		invoiceId := gjson.ParseBytes(body).Get("invoiceId").String()

		// got strike invoice - get actual LN invoice now. sigh.
		req, err = http.NewRequest("POST",
			"https://api.strike.me/v1/invoices/"+invoiceId+"/quote", jpayload)

		if err != nil {
			return "", err
		}
		req.Header.Add("Content-Type", "application/json")
		req.Header.Add("Accept", "application/json")
		req.Header.Add("Authorization", "Bearer "+backend.Key)

		res, err = client.Do(req)
		if err != nil {
			return "", err
		}
		defer res.Body.Close()

		body, err = io.ReadAll(res.Body)
		if err != nil {
			return "", err
		}

		lnInvoice := gjson.ParseBytes(body).Get("lnInvoice").String()

		return lnInvoice, nil

	case PhoenixParams:
		payload := url.Values{}

		if params.Description != "" {
			if params.UseDescriptionHash {
				payload.Set("descriptionHash", hexh)
			} else {
				payload.Set("description", params.Description)
			}

		} else {
			payload.Set("description", "created by makeinvoice")
		}

		payload.Add("amountSat", fmt.Sprintf("%d", params.Msatoshi/1000))

		client := &http.Client{}
		req, err := http.NewRequest(
			"POST",
			"http://"+backend.Host+"/createinvoice",
			strings.NewReader(payload.Encode()),
		)

		if err != nil {
			return "", err
		}

		keyb64 := base64.StdEncoding.EncodeToString([]byte("phoenix-cli:"+backend.Key))

		req.Header.Add("Authorization", "Basic "+keyb64)
		req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

		res, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer res.Body.Close()

		if res.StatusCode >= 300 {
			body, _ := io.ReadAll(res.Body)
			text := string(body)
			if len(text) > 300 {
				text = text[:300]
			}
			return "", fmt.Errorf("call to phoenix failed (%d): %s", res.StatusCode, text)
		}

		body, err := io.ReadAll(res.Body)
		if err != nil {
			return "", err
		}

		lnInvoice := gjson.ParseBytes(body).Get("serialized").String()

		return lnInvoice, nil

	case CommandoParams:
		ln := lnsocket.LNSocket{}
		ln.GenKey()

		err := ln.ConnectAndInit(backend.Host, backend.NodeId)
		if err != nil {
			return "", err
		}
		defer ln.Disconnect()

		label := params.Label
		if label == "" {
			label = makeRandomLabel()
		}

		invoiceParams := map[string]interface{}{
			"amount_msat": params.Msatoshi,
			"label":       label,
			"description": params.Description,
		}
		if params.UseDescriptionHash {
			invoiceParams["deschashonly"] = true
		}
		jparams, _ := json.Marshal(invoiceParams)

		body, err := ln.Rpc(backend.Rune, "invoice", string(jparams))
		if err != nil {
			return "", err
		}

		resErr := gjson.Get(body, "error")
		if resErr.Type != gjson.Null {
			if resErr.Type == gjson.JSON {
				return "", errors.New(resErr.Get("message").String())
			} else if resErr.Type == gjson.String {
				return "", errors.New(resErr.String())
			}
			return "", fmt.Errorf("Unknown commando error: '%v'", resErr)
		}

		invoice := gjson.Get(body, "result.bolt11")
		if invoice.Type != gjson.String {
			return "", fmt.Errorf("No bolt11 result found in invoice response, got %v", body)
		}

		return invoice.String(), nil
	}

	return "", errors.New("missing backend params")
}

func makeRandomLabel() string {
	return "makeinvoice/" + strconv.FormatInt(time.Now().Unix(), 16)
}
