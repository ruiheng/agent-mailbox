package mailbox

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
)

type outputFormat uint8

const (
	outputFormatText outputFormat = iota
	outputFormatJSON
	outputFormatYAML
)

type outputFlags struct {
	json bool
	yaml bool
}

func (f *outputFlags) register(fs *flag.FlagSet, jsonUsage, yamlUsage string) {
	fs.BoolVar(&f.json, "json", false, jsonUsage)
	fs.BoolVar(&f.yaml, "yaml", false, yamlUsage)
}

func (f outputFlags) resolve() (outputFormat, error) {
	if f.json && f.yaml {
		return outputFormatText, errors.New("--json and --yaml are mutually exclusive")
	}
	if f.yaml {
		return outputFormatYAML, nil
	}
	if f.json {
		return outputFormatJSON, nil
	}
	return outputFormatText, nil
}

func (a *App) writeStructuredOutput(format outputFormat, value any) error {
	switch format {
	case outputFormatJSON:
		encoder := json.NewEncoder(a.stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	case outputFormatYAML:
		return writeYAML(a.stdout, value)
	default:
		return fmt.Errorf("unsupported structured output format %d", format)
	}
}

func (a *App) writeListedDeliveryText(delivery ListedDelivery) error {
	_, err := fmt.Fprintf(
		a.stdout,
		"delivery_id=%s recipient_address=%s state=%s visible_at=%s subject=%q\n",
		delivery.DeliveryID,
		delivery.RecipientAddress,
		delivery.State,
		delivery.VisibleAt,
		delivery.Subject,
	)
	return err
}

func (a *App) newWatchEmitter(format outputFormat) (func(ListedDelivery) error, error) {
	switch format {
	case outputFormatText:
		return a.writeListedDeliveryText, nil
	case outputFormatJSON:
		encoder := json.NewEncoder(a.stdout)
		return func(delivery ListedDelivery) error {
			return encoder.Encode(delivery)
		}, nil
	case outputFormatYAML:
		return func(delivery ListedDelivery) error {
			if _, err := io.WriteString(a.stdout, "---\n"); err != nil {
				return err
			}
			return writeYAML(a.stdout, delivery)
		}, nil
	default:
		return nil, fmt.Errorf("unsupported watch output format %d", format)
	}
}
