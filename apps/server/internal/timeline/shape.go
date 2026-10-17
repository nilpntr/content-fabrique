package timeline

import (
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"strings"
)

// shapeCheck walks a generic document (numbers as json.Number) next to the Go types it will be decoded into, and finds
// the two things encoding/json is too forgiving about. They are the reason this runs before the typed decode:
//
//   - A key that matches a field only when case is ignored ("ID", "At_Ms"). encoding/json accepts it; the schema does
//     not, so a spec must not get through the Go types either.
//   - A whole number written with a decimal point or an exponent (3000.0, 3e3) where the Go type is an integer. The
//     schema accepts it, because 3000.0 is an integer in JSON Schema; decoding into an int64 does not.
//
// Both come back with the JSON Pointer of the offending key or value, so the caller can say exactly where. Unknown
// keys are left to the strict decode.
func shapeCheck(doc any) error {
	return walkShape(doc, reflect.TypeFor[Spec](), "")
}

var (
	trackIface = reflect.TypeFor[Track]()
	audioIface = reflect.TypeFor[AudioItem]()

	trackVariants = map[string]reflect.Type{
		string(TrackVideo):    reflect.TypeFor[VideoTrack](),
		string(TrackText):     reflect.TypeFor[TextTrack](),
		string(TrackCaptions): reflect.TypeFor[CaptionsTrack](),
		string(TrackAudio):    reflect.TypeFor[AudioTrack](),
	}
	audioVariants = map[string]reflect.Type{
		string(AudioTrendSound):    reflect.TypeFor[TrendSound](),
		string(AudioLicensedTrack): reflect.TypeFor[LicensedTrack](),
		string(AudioCreatorAudio):  reflect.TypeFor[CreatorAudio](),
	}
)

func walkShape(v any, t reflect.Type, path string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t {
	case trackIface:
		return walkUnion(v, "type", trackVariants, path)
	case audioIface:
		return walkUnion(v, "kind", audioVariants, path)
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil // the schema or the typed decode reports a wrong type
		}
		return walkObject(obj, t, "", path)
	case reflect.Slice:
		arr, ok := v.([]any)
		if !ok {
			return nil
		}
		for i, e := range arr {
			if err := walkShape(e, t.Elem(), fmt.Sprintf("%s/%d", path, i)); err != nil {
				return err
			}
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if n, ok := v.(json.Number); ok {
			return checkInteger(n, path)
		}
	default:
	}
	return nil
}

// walkUnion picks the variant by its discriminator and walks the object as that type. disc is the name of the
// discriminator key, which every variant accepts.
func walkUnion(v any, disc string, variants map[string]reflect.Type, path string) error {
	obj, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	tag, has := obj[disc]
	if !has {
		for k := range obj {
			if strings.EqualFold(k, disc) {
				return caseError(path, k, disc)
			}
		}
		return nil
	}
	name, _ := tag.(string)
	typ, known := variants[name]
	if !known {
		return nil
	}
	return walkObject(obj, typ, disc, path)
}

// walkObject checks the keys of obj against the JSON names of struct type t (plus the discriminator, when there is
// one) and walks the values that have a matching field.
func walkObject(obj map[string]any, t reflect.Type, disc, path string) error {
	fields := map[string]reflect.Type{}
	jsonFields(t, fields)
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		ft, ok := fields[k]
		if !ok {
			if k == disc {
				continue
			}
			for name := range fields {
				if strings.EqualFold(k, name) {
					return caseError(path, k, name)
				}
			}
			if disc != "" && strings.EqualFold(k, disc) {
				return caseError(path, k, disc)
			}
			continue
		}
		if err := walkShape(obj[k], ft, path+"/"+escapePointer(k)); err != nil {
			return err
		}
	}
	return nil
}

// jsonFields collects the JSON names of the fields of a struct type, flattening embedded structs as encoding/json does.
func jsonFields(t reflect.Type, into map[string]reflect.Type) {
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			if ft := f.Type; ft.Kind() == reflect.Struct {
				jsonFields(ft, into)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		into[name] = f.Type
	}
}

func caseError(path, got, want string) error {
	return &DecodeError{
		Path:    path + "/" + escapePointer(got),
		Message: fmt.Sprintf("the key %q must be written %q: keys are case-sensitive", got, want),
	}
}

// checkInteger accepts only plain integer literals (-12, 0, 3000) for an integer field.
func checkInteger(n json.Number, path string) error {
	s := n.String()
	digits := strings.TrimPrefix(s, "-")
	plain := digits != ""
	for _, r := range digits {
		if r < '0' || r > '9' {
			plain = false
			break
		}
	}
	if plain {
		return nil
	}
	// The length guard keeps an absurd exponent (1e999999) from making big.Rat build a huge number.
	if len(s) <= 40 {
		if r, ok := new(big.Rat).SetString(s); ok && r.IsInt() {
			return &DecodeError{
				Path:    path,
				Message: fmt.Sprintf("the whole number %s is written with a decimal point or an exponent; write it as %s", s, r.Num().String()),
			}
		}
	}
	return &DecodeError{Path: path, Message: fmt.Sprintf("%s must be a whole number", s)}
}

func escapePointer(s string) string {
	return pointer([]string{s})[1:]
}
