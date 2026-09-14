package nats_test

import (
	"fmt"
	"testing"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/notify"
	"github.com/kartaladev/hmntsk/notify/nats"
)

// signalsFor builds n signals at distinct microsecond instants, as the codec
// writes them, so that a decoded message compares equal to what was sent.
func signalsFor(n int) []notify.Signal {
	base := time.Date(2026, 9, 14, 8, 30, 0, 0, time.UTC)
	signals := make([]notify.Signal, n)

	for i := range signals {
		signals[i] = notify.Signal{
			Recipient: fmt.Sprintf("user-%04d", i),
			Change:    notify.ChangeCreated,
			At:        base.Add(time.Duration(i) * time.Microsecond),
		}
	}

	return signals
}

func TestBroadcast(t *testing.T) {
	t.Parallel()

	var serverURL string

	publisher := nats.RunTestNATS(t, nats.WithTestURL(&serverURL))

	subscriber, err := natsgo.Connect(serverURL)
	require.NoError(t, err)
	t.Cleanup(subscriber.Close)

	closed, err := natsgo.Connect(serverURL)
	require.NoError(t, err)
	closed.Close()

	type testCase struct {
		name    string
		conn    *natsgo.Conn
		signals []notify.Signal
		assert  func(t *testing.T, messages [][]notify.Signal, err error)
	}

	cases := []testCase{
		{
			name:    "one signal is one message in the notify format",
			conn:    publisher,
			signals: signalsFor(1),
			assert: func(t *testing.T, messages [][]notify.Signal, err error) {
				require.NoError(t, err)
				assert.Equal(t, [][]notify.Signal{signalsFor(1)}, messages)
			},
		},
		{
			name:    "a broadcast larger than one message is split at the maximum",
			conn:    publisher,
			signals: signalsFor(2*nats.MaxSignalsPerMessage + 1),
			assert: func(t *testing.T, messages [][]notify.Signal, err error) {
				require.NoError(t, err)

				all := signalsFor(2*nats.MaxSignalsPerMessage + 1)
				assert.Equal(t, [][]notify.Signal{
					all[:nats.MaxSignalsPerMessage],
					all[nats.MaxSignalsPerMessage : 2*nats.MaxSignalsPerMessage],
					all[2*nats.MaxSignalsPerMessage:],
				}, messages)
			},
		},
		{
			name:    "no signals publish nothing",
			conn:    publisher,
			signals: nil,
			assert: func(t *testing.T, messages [][]notify.Signal, err error) {
				require.NoError(t, err)
				assert.Empty(t, messages)
			},
		},
		{
			name:    "a closed connection is a publish error",
			conn:    closed,
			signals: signalsFor(1),
			assert: func(t *testing.T, _ [][]notify.Signal, err error) {
				require.ErrorIs(t, err, nats.ErrPublish)
				require.ErrorIs(t, err, natsgo.ErrConnectionClosed)

				var publish *nats.PublishError
				require.ErrorAs(t, err, &publish)
				assert.Equal(t, 1, publish.Signals)
			},
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			subject := fmt.Sprintf("test.broadcast.%d", i)

			sub, err := subscriber.SubscribeSync(subject)
			require.NoError(t, err)
			t.Cleanup(func() { _ = sub.Unsubscribe() })
			require.NoError(t, subscriber.Flush(), "the raw subscription is registered before anything is published")

			b, err := nats.NewBroadcaster(tc.conn, nats.WithSubject(subject))
			require.NoError(t, err)

			err = b.Broadcast(t.Context(), tc.signals)

			// A sentinel published after the broadcast marks the end of what it
			// sent, so the test reads exactly the broadcast's messages.
			require.NoError(t, publisher.Flush())
			require.NoError(t, publisher.Publish(subject, []byte("end")))
			require.NoError(t, publisher.Flush())

			var messages [][]notify.Signal

			for {
				msg, nextErr := sub.NextMsg(5 * time.Second)
				require.NoError(t, nextErr)

				if string(msg.Data) == "end" {
					break
				}

				decoded, decodeErr := notify.DecodeSignals(msg.Data)
				require.NoError(t, decodeErr, "every message is in the notify signal format")

				messages = append(messages, decoded)
			}

			tc.assert(t, messages, err)
		})
	}
}
