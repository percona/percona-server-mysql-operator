package config

import (
	"io"

	"github.com/go-ini/ini"
	"github.com/pkg/errors"
)

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
