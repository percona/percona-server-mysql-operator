package config

import (
	"encoding/json"
	"io"

	"github.com/go-ini/ini"
	"github.com/pkg/errors"
)

// Section is a wrapper around ini.Section to provide additional methods.
type Section struct {
	ini.Section
}

var EmptySection = Section{*ini.Empty(ini.LoadOptions{AllowBooleanKeys: true}).Section("")}

type NewSectionOpts struct {
	SectionName string
	Section     *ini.Section
}

func NewSection(o NewSectionOpts) (*Section, error) {
	if o.Section != nil {
		return &Section{*o.Section}, nil
	}
	f := ini.Empty(ini.LoadOptions{AllowBooleanKeys: true})
	if o.SectionName == "" {
		return &Section{*f.Section("")}, nil
	}
	sec, err := f.NewSection(o.SectionName)
	if err != nil {
		return nil, errors.Wrapf(err, "create section %s", o.SectionName)
	}
	return &Section{*sec}, nil
}

func (s *Section) IntoJSON() ([]byte, error) {
	return json.Marshal(s.KeysHash())
}

func (s *Section) FromJSON(in io.Reader, sectionName string) error {
	b, err := io.ReadAll(in)
	if err != nil {
		return errors.Wrap(err, "read input")
	}

	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return errors.Wrap(err, "unmarshal json")
	}

	f := ini.Empty(ini.LoadOptions{AllowBooleanKeys: true})
	sec, err := f.NewSection(sectionName)
	if err != nil {
		return errors.Wrap(err, "create section")
	}

	for name, val := range m {
		if _, err := sec.NewKey(name, val); err != nil {
			return errors.Wrapf(err, "create key %s", name)
		}
	}
	s.Section = *sec
	return nil
}

// ParseSection loads an ini file and returns the named section.
func ParseSection(myCnfFile io.ReadCloser, sectionName string) (*ini.Section, error) {
	cfg, err := ini.LoadSources(ini.LoadOptions{AllowBooleanKeys: true}, myCnfFile)
	if err != nil {
		return nil, errors.Wrap(err, "load ini")
	}

	if sectionName != "" && !cfg.HasSection(sectionName) {
		sectionName = ""
	}

	section, err := cfg.GetSection(sectionName)
	if err != nil {
		return nil, errors.Wrapf(err, "get section %s", sectionName)
	}

	return section, nil
}

// Changed returns a list of keys that are present in both, but have different values.
func (a *Section) Changed(b Section) []string {
	result := []string{}
	for _, k := range a.Keys() {
		if !b.HasKey(k.Name()) {
			continue
		}

		bk, _ := b.GetKey(k.Name()) // key not exists is the only error returned, but we already check it first
		if k.Value() != bk.Value() {
			result = append(result, k.Name())
		}
	}
	return result
}

// Subtract returns a list of keys that are present in a but not in b.
func (a *Section) Subtract(b Section) []string {
	result := []string{}
	for _, k := range a.Keys() {
		if !b.HasKey(k.Name()) {
			result = append(result, k.Name())
		}
	}
	return result
}

// GetKeyValue retrieves the string value of the given option from an ini section.
func GetKeyValue(myCnf *ini.Section, option string) (string, error) {
	var key *ini.Key
	var err error

	if myCnf.HasKey(option) {
		key, err = myCnf.GetKey(option)
	} else if myCnf.HasKey("loose_" + option) {
		key, err = myCnf.GetKey("loose_" + option)
	}
	if err != nil {
		return "", errors.Wrapf(err, "get %s", option)
	}

	if key == nil {
		return "", nil
	}

	return key.Value(), nil
}
