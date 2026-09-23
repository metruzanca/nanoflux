package plugin

import (
	"errors"
	"fmt"

	"github.com/metruzanca/nanoflux/pluginapi"
)

var (
	errNotAFetcher = errors.New("plugin does not implement Fetcher")
	errNoName      = errors.New("plugin reported an empty name")
)

func errIncompatibleAPI(got string) error {
	return fmt.Errorf("plugin API version %q does not match host %q", got, pluginapi.APIVersion)
}
