// Copyright © 2016 Casa Platform
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"crypto/tls"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/casaplatform/casa"
	"github.com/casaplatform/casa/cmd/casa/environment"
	"github.com/casaplatform/mqtt"
	"github.com/gomqtt/broker"
	"github.com/gomqtt/packet"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func init() {
	RootCmd.AddCommand(serverCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// serverCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// serverCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")

	rand.Seed(time.Now().UnixNano())
}

// serverCmd represents the server command
var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Starts the Casa server with an internal MQTT broker",
	RunE: func(cmd *cobra.Command, args []string) error {
		// why an "environment" package? http://www.jerf.org/iri/post/2929
		// Copy the global Environment so we get
		// the registered services. This can go away after plugins
		// are available in Go.

		// Use the environment packages's global Environment
		env := environment.Env
		brokerlogger := &brokerLogger{
			Logger: env.Logger,
		}
		var cert tls.Certificate
		var err error
		if viper.GetBool("MQTT.TLS.Enabled") {
			cert, err = tls.LoadX509KeyPair(
				viper.GetString("MQTT.TLS.Certificate"),
				viper.GetString("MQTT.TLS.Key"))

			if err != nil {
				return errors.Wrap(err, "failed loading TLS certificate")
			}
		}

		// If the user has specifed authorized user names to connect,
		// we need to add our own to the list so that the services
		// can connect to the broker.
		users := viper.GetStringMapString("MQTT.Users")
		var serviceUser, servicePass string
		var usingUsers bool
		if len(users) > 0 {
			serviceUser = getRand()
			servicePass = getRand()
			usingUsers = true
			users[serviceUser] = servicePass
		}

		// Create a new MessageBus by running our own MQTT broker
		bus, err := mqtt.New(
			mqtt.TLS(cert),
			mqtt.Users(users),
			mqtt.ListenOn(viper.GetStringSlice("MQTT.Listen")...),
			mqtt.ListenOn("tcp://127.0.0.1:1883"),
			mqtt.BrokerLogger(brokerlogger.Log),
		)

		if err != nil {
			return errors.Wrap(err, "Failed to create message bus")
		}

		// Set the remainder of the environment up
		env.WithOptions(
			environment.WithBus(bus),
			environment.WithViper(viper.GetViper()),
			environment.WithRegistry(environment.Env.ServiceRegistry),
		)

		// Start listening for control-c and cleanly exit when called
		c := make(chan os.Signal)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		go func() {
			sig := <-c
			env.Log("\nsignal: ", sig)
			var status int
			for key, s := range env.GetAllServices() {
				if !env.GetBool("Services." + key + ".Enabled") {
					continue
				}

				env.Log("Stopping service", key)
				err := s.Stop()
				if err != nil {
					env.Log("Error stopping service", key, "::", err)
					status = 1
				}
			}

			err := env.MessageBus.Close()
			if err != nil {
				env.Log("Error closing bus", err)
				status = 1
			}
			os.Exit(status)
		}()

		for key := range env.GetStringMap("Services") {
			if env.GetBool("Services." + key + ".Enabled") {
				config := env.Sub("Services." + key)
				if usingUsers {
					config.Set("MQTT.User", serviceUser)
					config.Set("MQTT.Pass", servicePass)
				}

				svc := env.GetService(key)
				if svc == nil {
					env.Log("Unsupported service: " + key)
					continue
				}

				svc.UseLogger(env)

				env.Log("Starting service: " + key)

				c := make(chan error, 1)
				go func(key string, ch chan error) {
					ch <- svc.Start(config)
				}(key, c)

				select {
				case err := <-c:
					if err != nil {
						env.Log("Failed starting", key, "service")
						env.Log(err)
						continue
					}
					env.Log(key, "service started")
				case <-time.After(1 * time.Second):
					env.Log("Timeout while starting service")
				}
			}
		}

		for {
			// Loop forever!
			runtime.Gosched() // Play nice with go routines
		}
	},
}

var letterRunes = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

func getRand() string {
	b := make([]rune, rand.Intn(15))
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}

// handles logging for the gomqqt.Broker
type brokerLogger struct {
	casa.Logger
}

// BrokerLogger logs errors from the MQTT broker
func (bl *brokerLogger) Log(event broker.LogEvent, client *broker.Client,
	pkt packet.Packet, message *packet.Message, err error) {
	if err != nil {
		//fmt.Printf("%#v\n", err)
		switch err.(type) {
		case *net.OpError:
			// These errors happen all the time as clients come and
			// go, best to just ignore them...
			//bl.Logger.Log("net error:", err)
		default:
			bl.Logger.Log("New error encountered:")
			bl.Logger.Log(err)
			fmt.Printf("%#v\n", err)
		}
	}

	// Added to sort out later
	switch event {
	case broker.NewConnection:
	case broker.PacketReceived:
	case broker.MessagePublished:
	case broker.MessageForwarded:
	case broker.PacketSent:
	case broker.LostConnection:
	case broker.TransportError:
	case broker.SessionError:
	case broker.BackendError:
	case broker.ClientError:
	}

	if pkt != nil {
		switch pkt.Type() {
		case packet.CONNECT:
		case packet.CONNACK:
		case packet.PUBLISH:
		case packet.PUBACK:
		case packet.PUBREC:
		case packet.PUBREL:
		case packet.PUBCOMP:
		case packet.SUBSCRIBE:
		case packet.SUBACK:
		case packet.UNSUBSCRIBE:
		case packet.UNSUBACK:
		case packet.PINGREQ:
		case packet.PINGRESP:
		case packet.DISCONNECT:
		}
	}
}
