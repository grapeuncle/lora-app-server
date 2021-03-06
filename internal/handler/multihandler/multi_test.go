package multihandler

import (
	"bytes"
	"encoding/json"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/brocaar/lora-app-server/internal/common"
	"github.com/brocaar/lora-app-server/internal/handler"
	"github.com/brocaar/lora-app-server/internal/handler/httphandler"
	"github.com/brocaar/lora-app-server/internal/handler/mqtthandler"
	"github.com/brocaar/lora-app-server/internal/storage"
	"github.com/brocaar/lora-app-server/internal/test"
	"github.com/brocaar/lorawan"
	mqtt "github.com/eclipse/paho.mqtt.golang"
	. "github.com/smartystreets/goconvey/convey"
)

type testHTTPHandler struct {
	requests chan *http.Request
}

func (h *testHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := ioutil.ReadAll(r.Body)
	r.Body = ioutil.NopCloser(bytes.NewReader(b))
	h.requests <- r
	w.WriteHeader(http.StatusOK)
}

func TestHandler(t *testing.T) {
	conf := test.GetConfig()

	Convey("Given an MQTT client and handler, Redis and PostgreSQL databases and test http handler", t, func() {
		opts := mqtt.NewClientOptions().AddBroker(conf.MQTTServer).SetUsername(conf.MQTTUsername).SetPassword(conf.MQTTPassword)
		c := mqtt.NewClient(opts)
		token := c.Connect()
		token.Wait()
		So(token.Error(), ShouldBeNil)

		common.RedisPool = storage.NewRedisPool(conf.RedisURL)
		test.MustFlushRedis(common.RedisPool)

		db, err := storage.OpenDatabase(conf.PostgresDSN)
		So(err, ShouldBeNil)
		common.DB = db
		test.MustResetDB(common.DB)

		h := testHTTPHandler{
			requests: make(chan *http.Request, 100),
		}
		server := httptest.NewServer(&h)
		defer server.Close()

		mqttMessages := make(chan mqtt.Message, 100)
		token = c.Subscribe("#", 0, func(c mqtt.Client, msg mqtt.Message) {
			mqttMessages <- msg
		})
		token.Wait()
		So(token.Error(), ShouldBeNil)

		mqttHandler, err := mqtthandler.NewHandler(conf.MQTTServer, conf.MQTTUsername, conf.MQTTPassword, "")
		So(err, ShouldBeNil)

		Convey("Given an organization, application with http integration and node", func() {
			org := storage.Organization{
				Name: "test-org",
			}
			So(storage.CreateOrganization(db, &org), ShouldBeNil)

			app := storage.Application{
				OrganizationID: org.ID,
				Name:           "test-app",
			}
			So(storage.CreateApplication(db, &app), ShouldBeNil)

			config := httphandler.HandlerConfig{
				DataUpURL:            server.URL + "/rx",
				JoinNotificationURL:  server.URL + "/join",
				ACKNotificationURL:   server.URL + "/ack",
				ErrorNotificationURL: server.URL + "/error",
			}
			configJSON, err := json.Marshal(config)
			So(err, ShouldBeNil)

			So(storage.CreateIntegration(db, &storage.Integration{
				ApplicationID: app.ID,
				Kind:          HTTPHandlerKind,
				Settings:      configJSON,
			}), ShouldBeNil)

			node := storage.Node{
				ApplicationID: app.ID,
				Name:          "test-node",
				DevEUI:        lorawan.EUI64{1, 1, 1, 1, 1, 1, 1, 1},
				AppEUI:        lorawan.EUI64{2, 2, 2, 2, 2, 2, 2, 2},
				AppKey:        lorawan.AES128Key{3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3},
			}
			So(storage.CreateNode(db, node), ShouldBeNil)

			Convey("Getting the multi-handler for the created application", func() {
				multiHandler := NewHandler(mqttHandler)
				defer multiHandler.Close()

				Convey("Calling SendDataUp", func() {
					So(multiHandler.SendDataUp(handler.DataUpPayload{
						ApplicationID: app.ID,
						DevEUI:        node.DevEUI,
					}), ShouldBeNil)

					Convey("Then the payload was sent to both the MQTT and HTTP handler", func() {
						So(mqttMessages, ShouldHaveLength, 1)
						msg := <-mqttMessages
						So(msg.Topic(), ShouldEqual, "application/1/node/0101010101010101/rx")

						So(h.requests, ShouldHaveLength, 1)
						req := <-h.requests
						So(req.URL.Path, ShouldEqual, "/rx")
					})
				})

				Convey("Calling SendJoinNotification", func() {
					So(multiHandler.SendJoinNotification(handler.JoinNotification{
						ApplicationID: app.ID,
						DevEUI:        node.DevEUI,
					}), ShouldBeNil)

					Convey("Then the payload was sent to both the MQTT and HTTP handler", func() {
						So(mqttMessages, ShouldHaveLength, 1)
						msg := <-mqttMessages
						So(msg.Topic(), ShouldEqual, "application/1/node/0101010101010101/join")

						So(h.requests, ShouldHaveLength, 1)
						req := <-h.requests
						So(req.URL.Path, ShouldEqual, "/join")
					})
				})

				Convey("Calling SendACKNotification", func() {
					So(multiHandler.SendACKNotification(handler.ACKNotification{
						ApplicationID: app.ID,
						DevEUI:        node.DevEUI,
					}), ShouldBeNil)

					Convey("Then the payload was sent to both the MQTT and HTTP handler", func() {
						So(mqttMessages, ShouldHaveLength, 1)
						msg := <-mqttMessages
						So(msg.Topic(), ShouldEqual, "application/1/node/0101010101010101/ack")

						So(h.requests, ShouldHaveLength, 1)
						req := <-h.requests
						So(req.URL.Path, ShouldEqual, "/ack")
					})
				})

				Convey("Calling SendErrorNotification", func() {
					So(multiHandler.SendErrorNotification(handler.ErrorNotification{
						ApplicationID: app.ID,
						DevEUI:        node.DevEUI,
					}), ShouldBeNil)

					Convey("Then the payload was sent to both the MQTT and HTTP handler", func() {
						So(mqttMessages, ShouldHaveLength, 1)
						msg := <-mqttMessages
						So(msg.Topic(), ShouldEqual, "application/1/node/0101010101010101/error")

						So(h.requests, ShouldHaveLength, 1)
						req := <-h.requests
						So(req.URL.Path, ShouldEqual, "/error")
					})
				})
			})
		})
	})
}
