package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SignalFormatVersion is the version of the wire format [EncodeSignals] writes:
//
//	{"v":1,"signals":[{"recipient":"alice","change":"created","at":"2026-09-14T08:30:00.123456Z"}]}
//
// Cross-instance broadcasters share it, so that a signal published through one
// instance reads the same everywhere. A message never carries a notification's
// title, links, data, kind or subject.
const SignalFormatVersion = 1

// ErrUnknownSignalFormat reports a message in a format version this library does
// not read. A broadcaster reports it rather than delivering the message.
var ErrUnknownSignalFormat = errors.New("notify: unknown signal format")

// signalMessage is the wire format.
type signalMessage struct {
	Version int          `json:"v"`
	Signals []wireSignal `json:"signals"`
}

// wireSignal is one signal on the wire, in the field order the format fixes.
type wireSignal struct {
	Recipient string    `json:"recipient"`
	Change    Change    `json:"change"`
	At        time.Time `json:"at"`
}

// EncodeSignals renders signals in the current wire format, with each instant in
// UTC at microsecond precision.
func EncodeSignals(signals []Signal) ([]byte, error) {
	message := signalMessage{Version: SignalFormatVersion, Signals: make([]wireSignal, 0, len(signals))}

	for _, signal := range signals {
		message.Signals = append(message.Signals, wireSignal{
			Recipient: signal.Recipient, Change: signal.Change, At: normalizeTime(signal.At),
		})
	}

	encoded, err := json.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("notify: encode signals: %w", err)
	}

	return encoded, nil
}

// DecodeSignals reads a message in the current wire format. A message of
// another version, or with none, is an error matching [ErrUnknownSignalFormat];
// malformed JSON is a plain decode error.
func DecodeSignals(data []byte) ([]Signal, error) {
	var message signalMessage
	if err := json.Unmarshal(data, &message); err != nil {
		return nil, fmt.Errorf("notify: decode signals: %w", err)
	}

	if message.Version != SignalFormatVersion {
		return nil, fmt.Errorf("%w: version %d, expected %d", ErrUnknownSignalFormat, message.Version, SignalFormatVersion)
	}

	signals := make([]Signal, 0, len(message.Signals))
	for _, signal := range message.Signals {
		signals = append(signals, Signal{Recipient: signal.Recipient, Change: signal.Change, At: normalizeTime(signal.At)})
	}

	return signals, nil
}
