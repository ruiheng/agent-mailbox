package mailbox

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

func writeYAML(w io.Writer, value any) error {
	if err := writeYAMLValue(w, reflect.ValueOf(value), 0); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\n")
	return err
}

func writeYAMLValue(w io.Writer, value reflect.Value, indent int) error {
	scalar, ok, err := yamlScalar(value)
	if err != nil {
		return err
	}
	if ok {
		_, err := io.WriteString(w, scalar)
		return err
	}

	value = yamlConcreteValue(value)
	if !value.IsValid() {
		_, err := io.WriteString(w, "null")
		return err
	}

	switch value.Kind() {
	case reflect.Array, reflect.Slice:
		return writeYAMLSequence(w, value, indent)
	case reflect.Map:
		return writeYAMLMap(w, value, indent)
	case reflect.Struct:
		return writeYAMLStruct(w, value, indent)
	default:
		return fmt.Errorf("unsupported YAML value kind %s", value.Kind())
	}
}

func writeYAMLSequence(w io.Writer, value reflect.Value, indent int) error {
	if value.Len() == 0 {
		_, err := io.WriteString(w, "[]")
		return err
	}

	indentText := strings.Repeat(" ", indent)
	for index := range value.Len() {
		item := value.Index(index)
		scalar, ok, err := yamlScalar(item)
		if err != nil {
			return err
		}
		if ok {
			if _, err := fmt.Fprintf(w, "%s- %s", indentText, scalar); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(w, "%s-", indentText); err != nil {
				return err
			}
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
			if err := writeYAMLValue(w, item, indent+2); err != nil {
				return err
			}
		}
		if index < value.Len()-1 {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeYAMLMap(w io.Writer, value reflect.Value, indent int) error {
	if value.Len() == 0 {
		_, err := io.WriteString(w, "{}")
		return err
	}

	if value.Type().Key().Kind() != reflect.String {
		return fmt.Errorf("unsupported YAML map key kind %s", value.Type().Key().Kind())
	}

	keys := value.MapKeys()
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].String() < keys[j].String()
	})

	for index, key := range keys {
		mapValue := value.MapIndex(key)
		if err := writeYAMLField(w, indent, key.String(), mapValue); err != nil {
			return err
		}
		if index < len(keys)-1 {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeYAMLStruct(w io.Writer, value reflect.Value, indent int) error {
	fields := yamlStructFields(value)
	if len(fields) == 0 {
		_, err := io.WriteString(w, "{}")
		return err
	}

	for index, field := range fields {
		if err := writeYAMLField(w, indent, field.name, field.value); err != nil {
			return err
		}
		if index < len(fields)-1 {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeYAMLField(w io.Writer, indent int, name string, value reflect.Value) error {
	indentText := strings.Repeat(" ", indent)
	scalar, ok, err := yamlScalar(value)
	if err != nil {
		return err
	}
	if ok {
		_, err := fmt.Fprintf(w, "%s%s: %s", indentText, name, scalar)
		return err
	}

	if _, err := fmt.Fprintf(w, "%s%s:\n", indentText, name); err != nil {
		return err
	}
	return writeYAMLValue(w, value, indent+2)
}

func yamlScalar(value reflect.Value) (string, bool, error) {
	value = yamlConcreteValue(value)
	if !value.IsValid() {
		return "null", true, nil
	}

	switch value.Kind() {
	case reflect.String:
		text, err := json.Marshal(value.String())
		if err != nil {
			return "", false, fmt.Errorf("marshal YAML string: %w", err)
		}
		return string(text), true, nil
	case reflect.Bool:
		return strconv.FormatBool(value.Bool()), true, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(value.Int(), 10), true, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(value.Uint(), 10), true, nil
	case reflect.Float32:
		return strconv.FormatFloat(value.Float(), 'g', -1, 32), true, nil
	case reflect.Float64:
		return strconv.FormatFloat(value.Float(), 'g', -1, 64), true, nil
	default:
		return "", false, nil
	}
}

func yamlConcreteValue(value reflect.Value) reflect.Value {
	for value.IsValid() && (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) {
		if value.IsNil() {
			return reflect.Value{}
		}
		value = value.Elem()
	}
	return value
}

type yamlStructField struct {
	name  string
	value reflect.Value
}

func yamlStructFields(value reflect.Value) []yamlStructField {
	fields := make([]yamlStructField, 0, value.NumField())
	valueType := value.Type()
	for index := 0; index < value.NumField(); index++ {
		fieldType := valueType.Field(index)
		if !fieldType.IsExported() {
			continue
		}

		name, omitEmpty, skip := yamlFieldName(fieldType)
		if skip {
			continue
		}

		fieldValue := value.Field(index)
		if omitEmpty && fieldValue.IsZero() {
			continue
		}
		fields = append(fields, yamlStructField{
			name:  name,
			value: fieldValue,
		})
	}
	return fields
}

func yamlFieldName(field reflect.StructField) (string, bool, bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}

	name := field.Name
	omitEmpty := false
	if tag != "" {
		parts := strings.Split(tag, ",")
		if parts[0] != "" {
			name = parts[0]
		}
		for _, option := range parts[1:] {
			if option == "omitempty" {
				omitEmpty = true
			}
		}
	}
	return name, omitEmpty, false
}
