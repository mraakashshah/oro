package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"oro/pkg/factory"
)

// factoryJobPlist renders a bootstrap wrapper, never the command to execute.
// Path is the admitted Oro binary; Args identify its immutable request envelope
// and handshake socket. The actual command and result path remain inside that
// digest-bound envelope; the socket supplies objective-bound authority.
// Runtime admission verifies executable and
// filesystem ownership; the wrapper must obtain authenticated GO before effects.
func factoryJobPlist(invocation factory.HostInvocation) ([]byte, error) {
	if err := validateFactoryJob(invocation); err != nil {
		return nil, err
	}
	args := append([]string{invocation.Path}, invocation.Args...)
	args = append(args, "--deadline", invocation.Deadline.UTC().Format(time.RFC3339Nano),
		"--output-limit", strconv.FormatInt(invocation.OutputLimit, 10))
	argv := factoryJobXMLNode("array", "")
	for _, arg := range args {
		argv.Children = append(argv.Children, factoryJobXMLNode("string", arg))
	}
	properties := []struct {
		key   string
		value factoryJobXMLValue
	}{
		{"Label", factoryJobXMLNode("string", invocation.Scope.Name)},
		{"Program", factoryJobXMLNode("string", invocation.Path)},
		{"ProgramArguments", argv},
		{"WorkingDirectory", factoryJobXMLNode("string", invocation.Dir)},
		{"RunAtLoad", factoryJobXMLNode("true", "")},
		{"KeepAlive", factoryJobXMLNode("false", "")},
		{"AbandonProcessGroup", factoryJobXMLNode("false", "")},
		{"EnableGlobbing", factoryJobXMLNode("false", "")},
		{"ExitTimeOut", factoryJobXMLNode("integer", "5")},
		{"Umask", factoryJobXMLNode("integer", "63")},
		{"StandardInPath", factoryJobXMLNode("string", "/dev/null")},
		{"StandardOutPath", factoryJobXMLNode("string", "/dev/null")},
		{"StandardErrorPath", factoryJobXMLNode("string", "/dev/null")},
	}
	dict := factoryJobXMLNode("dict", "")
	for _, property := range properties {
		dict.Children = append(dict.Children, factoryJobXMLNode("key", property.key), property.value)
	}
	document := factoryJobXMLNode("plist", "")
	document.Version, document.Children = "1.0", []factoryJobXMLValue{dict}
	data, err := xml.MarshalIndent(document, "", "\t")
	if err != nil {
		return nil, fmt.Errorf("encode factory job plist: %w", err)
	}
	return append([]byte(xml.Header), data...), nil
}

func validateFactoryJob(invocation factory.HostInvocation) error {
	if err := validateFactoryJobScope(invocation.Scope); err != nil {
		return err
	}
	if !factoryJobAbsolutePath(invocation.Path) || !factoryJobAbsolutePath(invocation.Dir) {
		return errors.New("factory job requires clean absolute executable and private directory paths")
	}
	if len(invocation.Env) != 0 || len(invocation.Stdin) != 0 {
		return errors.New("factory job wrapper cannot receive environment overrides or stdin")
	}
	year := invocation.Deadline.UTC().Year()
	if invocation.Deadline.IsZero() || year < 1 || year > 9999 {
		return errors.New("factory job requires a representable nonzero deadline")
	}
	// Leave arithmetic headroom for both streams and encoded pending results.
	if invocation.OutputLimit <= 0 || invocation.OutputLimit > math.MaxInt64/4 {
		return errors.New("factory job requires a positive nonoverflowing output limit")
	}
	return validateFactoryJobArgs(invocation)
}

func validateFactoryJobScope(scope factory.ScopeHandle) error {
	if scope.Backend != "launchd" || (scope.Kind != "host" && scope.Kind != "observer") || scope.Generation == 0 {
		return errors.New("factory job requires a launchd host or observer scope with a positive generation")
	}
	if !factoryJobLowerHex(scope.ID, 32) || !factoryJobLowerHex(scope.Nonce, 32) {
		return errors.New("factory job requires 32-character lowercase hexadecimal scope identity and nonce")
	}
	parts := strings.Split(scope.Name, ".")
	if len(parts) != 7 || !factoryJobLowerHex(parts[3], 16) ||
		scope.Name != "dev.getoro.factory."+parts[3]+"."+scope.Kind+"."+scope.ID+"."+scope.Nonce {
		return errors.New("factory job label does not match its exact owned scope identity")
	}
	return nil
}

func validateFactoryJobArgs(invocation factory.HostInvocation) error {
	args := invocation.Args
	if len(args) != 8 || args[0] != "factory" || args[1] != "_host-exec" ||
		args[2] != "--request" || args[4] != "--request-sha256" || args[6] != "--socket" {
		return errors.New("factory job requires the exact hidden wrapper argument shape")
	}
	if args[3] != filepath.Join(invocation.Dir, "request.json") || !factoryJobLowerHex(args[5], 64) {
		return errors.New("factory job requires its private request.json and lowercase SHA-256 digest")
	}
	// Darwin sockaddr_un has 104 bytes including the terminating NUL. The
	// short socket parent may differ from Dir; runtime must verify its owner.
	if !factoryJobAbsolutePath(args[7]) || len(args[7]) >= 104 {
		return errors.New("factory job requires a clean absolute Unix socket path shorter than 104 bytes")
	}
	return nil
}

func factoryJobAbsolutePath(path string) bool {
	if !utf8.ValidString(path) || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return false
	}
	for _, value := range path {
		if unicode.IsControl(value) || value == 0xfffe || value == 0xffff {
			return false
		}
	}
	return true
}

func factoryJobLowerHex(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, digit := range value {
		if (digit < '0' || digit > '9') && (digit < 'a' || digit > 'f') {
			return false
		}
	}
	return true
}

type factoryJobXMLValue struct {
	XMLName  xml.Name
	Version  string               `xml:"version,attr,omitempty"`
	Text     string               `xml:",chardata"`
	Children []factoryJobXMLValue `xml:",any"`
}

func factoryJobXMLNode(kind, text string) factoryJobXMLValue {
	return factoryJobXMLValue{XMLName: xml.Name{Local: kind}, Text: text}
}
