package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	decodepay "github.com/nbd-wtf/ln-decodepay"
	"github.com/fiatjaf/makeinvoice"
	"github.com/tidwall/gjson"
)

var (
	TorProxyURL = "socks5://127.0.0.1:9050"
	Client = &http.Client{
		Timeout: 10 * time.Second,
	}
)

func WaitForInvoicePaid(payvalues LNURLPayValuesCustom, params *UserParams) {
	// Check for a minute if invoice is paid
	// Do we have an easier way to do  this? How does it work for other backends than lnbits.
	go func() {
		var backend makeinvoice.LNBackendParams

		switch params.Kind {
		case "sparko":
			backend = makeinvoice.SparkoParams{
				Host: params.Host,
				Key:  params.Key,
			}
		case "lnd":
			backend = makeinvoice.LNDParams{
				Host:     params.Host,
				Macaroon: params.Key,
			}
		case "lnbits":
			backend = makeinvoice.LNBitsParams{
				Host: params.Host,
				Key:  params.Key,
			}
		case "lnpay":
			backend = makeinvoice.LNPayParams{
				PublicAccessKey:  params.Pak,
				WalletInvoiceKey: params.Waki,
			}
		case "eclair":
			backend = makeinvoice.EclairParams{
				Host:     params.Host,
				Password: "",
			}
		case "commando":
			backend = makeinvoice.CommandoParams{
				Host:   params.Host,
				NodeId: params.NodeId,
				Rune:   params.Rune,
			}
		case "phoenix":
			backend = makeinvoice.PhoenixParams{
				Host:   params.Host,
				Key: params.Key,
			}
		}

		mip := makeinvoice.LNParams{
			//Msatoshi: int64(msat),
			Backend: backend,

			Label: params.Domain + "/" + strconv.FormatInt(time.Now().Unix(), 16),
		}

		defer func(prevTransport http.RoundTripper) {
			Client.Transport = prevTransport
		}(Client.Transport)

		specialTransport := &http.Transport{}

		// use a cert or skip TLS verification?
		if mip.Backend.GetCert() != "" {
			caCertPool := x509.NewCertPool()
			caCertPool.AppendCertsFromPEM([]byte(mip.Backend.GetCert()))
			specialTransport.TLSClientConfig = &tls.Config{RootCAs: caCertPool}
		} else {
			specialTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}

		// use a tor proxy?
		if mip.Backend.IsTor() {
			torURL, _ := url.Parse(TorProxyURL)
			specialTransport.Proxy = http.ProxyURL(torURL)
		}

		Client.Transport = specialTransport
		var maxiterations = 34
		var interval = time.Second
		ticker := time.NewTicker(interval)
		quit := make(chan struct{})

		for {
			select {
			case <-ticker.C:

				bolt11, _ := decodepay.Decodepay(payvalues.PR)
				switch backend := mip.Backend.(type) {

				case makeinvoice.LNDParams:
					req, err := http.NewRequest("GET",
						backend.Host+"/v1/invoice/"+bolt11.PaymentHash,
						nil)
					if err != nil {
						fmt.Print(err.Error())
						return
					}
					if b, err := base64.StdEncoding.DecodeString(backend.Macaroon); err == nil {
						backend.Macaroon = hex.EncodeToString(b)
					}
					req.Header.Set("Grpc-Metadata-macaroon", backend.Macaroon)
					resp, err := Client.Do(req)
					if err != nil {
						fmt.Print(err.Error())
						return
					}
					defer resp.Body.Close()

					b, err := io.ReadAll(resp.Body)
					if err != nil {
						fmt.Print(err.Error())
						return
					}

					if gjson.ParseBytes(b).Get("settled").String() == "true" {
						payvalues.PaidAt = time.Now()
						payvalues.Paid = true
					}

				case makeinvoice.LNBitsParams:

					client := &http.Client{}
					url := backend.Host + "/api/v1/payments/" + bolt11.PaymentHash
					req, _ := http.NewRequest("GET", url, nil)
					req.Header.Set("X-Api-Key", backend.Key)
					req.Header.Set("Content-type", "application/json")

					response, err := client.Do(req)

					//response, err := http.Get(backend.Host + "/api/v1/payments/" + bolt11.PaymentHash)
					if err != nil {
						fmt.Print(err.Error())
						return
					}

					responseData, err := io.ReadAll(response.Body)

					if err != nil {
						fmt.Print(err.Error())
						return
					}
					var jsonMap map[string]interface{}
					err = json.Unmarshal([]byte(string(responseData)), &jsonMap)

					if err != nil {
						fmt.Print(err.Error())
						return
					}

					if jsonMap["paid"].(bool) {

						payvalues.PaidAt = time.Now()
						fmt.Print("LnBits says paid..\n")
						fmt.Print("Payment hash:" + bolt11.PaymentHash + "\n")
						fmt.Println(string(responseData))
						payvalues.Paid = true

					} else {
						fmt.Print("Checking invoice..\n")
					}

				case makeinvoice.PhoenixParams:

					client := &http.Client{}
					url := "http://"+backend.Host+"/payments/incoming/"+bolt11.PaymentHash
					req, _ := http.NewRequest("GET", url, nil)

					keyb64 := base64.StdEncoding.EncodeToString([]byte("phoenix-cli:"+backend.Key))

					req.Header.Add("Authorization", "Basic "+keyb64)

					response, err := client.Do(req)

					if err != nil {
						fmt.Print(err.Error())
						return
					}

					responseData, err := io.ReadAll(response.Body)

					if err != nil {
						fmt.Print(err.Error())
						return
					}

					var jsonMap map[string]interface{}
					err = json.Unmarshal([]byte(string(responseData)), &jsonMap)

					if err != nil {
						fmt.Print(err.Error())
						return
					}

					if jsonMap["isPaid"].(bool) {

						payvalues.PaidAt = time.Now()
						fmt.Print("Phoenix says paid..\n")
						fmt.Print("Payment hash:" + bolt11.PaymentHash + "\n")
						fmt.Println(string(responseData))
						payvalues.Paid = true

					} else {
						fmt.Print("Checking invoice..\n")
					}

				case makeinvoice.LNPayParams:
					//TODO
				case makeinvoice.EclairParams:
					//TODO
				case makeinvoice.SparkoParams:
					//TODO
				case makeinvoice.CommandoParams:
					//TODO
				}
				//Timeout waiting for payment after maxiterations
				if maxiterations == 0 {
					log.Debug().Str("NIP57 wait for payment", bolt11.PaymentHash).Msg("Timed out")
					close(quit)
				}

				//If invoice is paid and DescriptionHash matches Nip57 DescriptionHash, publish Zap Nostr Event. This is rather a sanity check.
				if payvalues.Paid {
					var amount = bolt11.MSatoshi / 1000

					if payvalues.Nip57Receipt.Tags != nil {
						var descriptionTag = payvalues.Nip57Receipt.Tags.Find("description")

						if descriptionTag == nil {
							log.Warn().Msg("Receipt missing description tag")
							close(quit)
						}

						if bolt11.DescriptionHash == Nip57DescriptionHash(descriptionTag[1]) {
							publishNostrEvent(payvalues.Nip57Receipt, payvalues.Nip57ReceiptRelays)
							var satsr = "Sats"
							if amount == 1 {
								satsr = "Sat"
							}

							if params.Npub != "" && params.NotifyZapComment && payvalues.Comment != "" {
								if payvalues.Note != "" {
									go sendMessage(params.Npub, "Received Zap from "+payvalues.Sender+" with amount: "+strconv.FormatInt(amount, 10)+" "+satsr+" ⚡️ for note: "+payvalues.Note+" Comment: "+payvalues.Comment)

								} else {
									go sendMessage(params.Npub, "Received Profile Zap from "+payvalues.Sender+" with amount: "+strconv.FormatInt(amount, 10)+" "+satsr+" ⚡️. Comment: "+payvalues.Comment)
								}
							} else if params.Npub != "" && params.NotifyZaps {
								if payvalues.Note != "" {
									go sendMessage(params.Npub, "Received Zap from "+payvalues.Sender+" with amount: "+strconv.FormatInt(amount, 10)+" "+satsr+" ⚡️ for note: "+payvalues.Note)

								} else {
									go sendMessage(params.Npub, "Received Profile Zap from "+payvalues.Sender+" with amount: "+strconv.FormatInt(amount, 10)+" "+satsr+" ⚡️.")
								}
							}
							//payvalues.Nip57Receipt.String()
							log.Debug().Str("ZAPPED ⚡️", "Published zap on Nostr").Msg("Nostr")
							close(quit)
							return

						}
					} else if params.Npub != "" && params.NotifyNonZap {
						var amount = payvalues.ParsedInvoice.MSatoshi / 1000
						var satsr = "Sats"
						if amount == 1 {
							satsr = "Sat"
						}
						if payvalues.Comment != "" {
							go sendMessage(params.Npub, "Received Non-Zap! Amount: "+strconv.FormatInt(amount, 10)+" "+satsr+" ⚡️. Comment: "+payvalues.Comment)

						} else {
							go sendMessage(params.Npub, "Received Non-Zap! Amount: "+strconv.FormatInt(amount, 10)+" "+satsr+" ⚡️.")
						}
						log.Debug().Str("ZAPPED ⚡️", "Published zap on Nostr").Msg("Nostr")
						close(quit)
						return

					}

				}

				interval = interval * 17 / 10
				ticker.Reset(interval)
				maxiterations--

			case <-quit:
				ticker.Stop()
				return
			}
		}
	}()
}
