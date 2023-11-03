// Package dburl provides a standard, [net/url.URL] style mechanism for parsing
// and opening SQL database connection strings for Go. Provides standardized
// way to parse and open [URL]'s for popular databases PostgreSQL, MySQL, SQLite3,
// Oracle Database, Microsoft SQL Server, in addition to most other SQL
// databases with a publicly available Go driver.
//
// See the [package documentation README section] for more details.
//
// [package documentation README section]: https://pkg.go.dev/github.com/xo/dburl#section-readme
package dburl

import (
	"database/sql"
	"net/url"
	"os"
	"strings"
)

// Open takes a URL string, also known as a DSN, in the form of
// "protocol+transport://user:pass@host/dbname?option1=a&option2=b" and opens a
// standard [sql.DB] connection.
//
// See [Parse] for information on formatting URL strings to work properly with Open.
func Open(urlstr string) (*sql.DB, error) {
	u, err := Parse(urlstr)
	if err != nil {
		return nil, err
	}
	driver := u.Driver
	if u.GoDriver != "" {
		driver = u.GoDriver
	}
	return sql.Open(driver, u.DSN)
}

// URL wraps the standard [net/url.URL] type, adding OriginalScheme, Transport,
// Driver, Unaliased, and DSN strings.
type URL struct {
	// URL is the base [net/url.URL].
	url.URL
	// OriginalScheme is the original parsed scheme (ie, "sq", "mysql+unix", "sap", etc).
	OriginalScheme string
	// Transport is the specified transport protocol (ie, "tcp", "udp",
	// "unix", ...), if provided.
	Transport string
	// Driver is the non-aliased SQL driver name that should be used in a call
	// to [sql.Open].
	Driver string
	// GoDriver is the Go SQL driver name to use when opening a connection to
	// the database. Used by Microsoft SQL Server's azuresql:// URLs, as the
	// wire-compatible alias style uses a different syntax style.
	GoDriver string
	// UnaliasedDriver is the unaliased driver name.
	UnaliasedDriver string
	// DSN is the built connection "data source name" that can be used in a
	// call to [sql.Open].
	DSN string
	// hostPortDB will be set by Gen*() funcs after determining the host, port,
	// database.
	//
	// When empty, indicates that these values are not special, and can be
	// retrieved as the host, port, and path[1:] as usual.
	hostPortDB []string
}

// Parse parses a URL string, similar to the standard [net/url.Parse].
//
// Handles parsing OriginalScheme, Transport, Driver, Unaliased, and DSN
// fields.
//
// Note: if the URL has a Opaque component (ie, URLs not specified as
// "scheme://" but "scheme:"), and the database scheme does not support opaque
// components, Parse will attempt to re-process the URL as "scheme://<opaque>".
func Parse(urlstr string) (*URL, error) {
	// parse url
	v, err := url.Parse(urlstr)
	switch {
	case err != nil:
		return nil, err
	case v.Scheme == "":
		return nil, ErrInvalidDatabaseScheme
	}
	// create url
	u := &URL{
		URL:            *v,
		OriginalScheme: urlstr[:len(v.Scheme)],
		Transport:      "tcp",
	}
	// check for +transport in scheme
	var checkTransport bool
	if i := strings.IndexRune(u.Scheme, '+'); i != -1 {
		u.Transport = urlstr[i+1 : len(v.Scheme)]
		u.Scheme = u.Scheme[:i]
		checkTransport = true
	}
	// get dsn generator
	scheme, ok := schemeMap[u.Scheme]
	if !ok {
		return nil, ErrUnknownDatabaseScheme
	}
	// load real scheme for file:
	if scheme.Driver == "file" {
		typ, err := SchemeType(u.Opaque)
		if err == nil {
			if s, ok := schemeMap[typ]; ok {
				scheme = s
			}
		}
	}
	// if scheme does not understand opaque URLs, retry parsing after building
	// fully qualified URL
	if !scheme.Opaque && u.Opaque != "" {
		var q string
		if u.RawQuery != "" {
			q = "?" + u.RawQuery
		}
		var f string
		if u.Fragment != "" {
			f = "#" + u.Fragment
		}
		return Parse(u.OriginalScheme + "://" + u.Opaque + q + f)
	}
	if scheme.Opaque && u.Opaque == "" {
		// force Opaque
		u.Opaque, u.Host, u.Path, u.RawPath = u.Host+u.Path, "", "", ""
	} else if u.Host == "." || (u.Host == "" && strings.TrimPrefix(u.Path, "/") != "") {
		// force unix proto
		u.Transport = "unix"
	}
	// check proto
	if checkTransport || u.Transport != "tcp" {
		if scheme.Transport == TransportNone {
			return nil, ErrInvalidTransportProtocol
		}
		switch {
		case scheme.Transport&TransportAny != 0 && u.Transport != "",
			scheme.Transport&TransportTCP != 0 && u.Transport == "tcp",
			scheme.Transport&TransportUDP != 0 && u.Transport == "udp",
			scheme.Transport&TransportUnix != 0 && u.Transport == "unix":
		default:
			return nil, ErrInvalidTransportProtocol
		}
	}
	// set driver
	u.Driver, u.UnaliasedDriver = scheme.Driver, scheme.Driver
	if scheme.Override != "" {
		u.Driver = scheme.Override
	}
	// generate dsn
	if u.DSN, u.GoDriver, err = scheme.Generator(u); err != nil {
		return nil, err
	}
	return u, nil
}

// String satisfies the [fmt.Stringer] interface.
func (u *URL) String() string {
	p := &url.URL{
		Scheme:   u.OriginalScheme,
		Opaque:   u.Opaque,
		User:     u.User,
		Host:     u.Host,
		Path:     u.Path,
		RawPath:  u.RawPath,
		RawQuery: u.RawQuery,
		Fragment: u.Fragment,
	}
	return p.String()
}

// Short provides a short description of the user, host, and database.
func (u *URL) Short() string {
	if u.Scheme == "" {
		return ""
	}
	s := schemeMap[u.Scheme].Aliases[0]
	if u.Scheme == "odbc" || u.Scheme == "oleodbc" {
		n := u.Transport
		if v, ok := schemeMap[n]; ok {
			n = v.Aliases[0]
		}
		s += "+" + n
	} else if u.Transport != "tcp" {
		s += "+" + u.Transport
	}
	s += ":"
	if u.User != nil {
		if n := u.User.Username(); n != "" {
			s += n + "@"
		}
	}
	if u.Host != "" {
		s += u.Host
	}
	if u.Path != "" && u.Path != "/" {
		s += u.Path
	}
	if u.Opaque != "" {
		s += u.Opaque
	}
	return s
}

// Normalize returns the driver, host, port, database, and user name of a URL,
// joined with sep, populating blank fields with empty.
func (u *URL) Normalize(sep, empty string, cut int) string {
	s := []string{u.UnaliasedDriver, "", "", "", ""}
	if u.Transport != "tcp" && u.Transport != "unix" {
		s[0] += "+" + u.Transport
	}
	// set host port dbname fields
	if u.hostPortDB == nil {
		if u.Opaque != "" {
			u.hostPortDB = []string{u.Opaque, "", ""}
		} else {
			u.hostPortDB = []string{u.Hostname(), u.Port(), strings.TrimPrefix(u.Path, "/")}
		}
	}
	copy(s[1:], u.hostPortDB)
	// set user
	if u.User != nil {
		s[4] = u.User.Username()
	}
	// replace blank entries ...
	for i := 0; i < len(s); i++ {
		if s[i] == "" {
			s[i] = empty
		}
	}
	if cut > 0 {
		// cut to only populated fields
		i := len(s) - 1
		for ; i > cut; i-- {
			if s[i] != "" {
				break
			}
		}
		s = s[:i]
	}
	return strings.Join(s, sep)
}

// SchemeType returns the scheme type for a file on disk.
func SchemeType(name string) (string, error) {
	f, err := os.OpenFile(name, os.O_RDONLY, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	buf := make([]byte, 128)
	if _, err := f.Read(buf); err != nil {
		return "", err
	}
	for _, header := range headerTypes {
		if header.f(buf) {
			return header.driver, nil
		}
	}
	return "", ErrUnknownFileHeader
}

// Error is an error.
type Error string

// Error satisfies the error interface.
func (err Error) Error() string {
	return string(err)
}

// Error values.
const (
	// ErrInvalidDatabaseScheme is the invalid database scheme error.
	ErrInvalidDatabaseScheme Error = "invalid database scheme"
	// ErrUnknownDatabaseScheme is the unknown database type error.
	ErrUnknownDatabaseScheme Error = "unknown database scheme"
	// ErrUnknownFileHeader is the unknown file header error.
	ErrUnknownFileHeader Error = "unknown file header"
	// ErrInvalidTransportProtocol is the invalid transport protocol error.
	ErrInvalidTransportProtocol Error = "invalid transport protocol"
	// ErrRelativePathNotSupported is the relative paths not supported error.
	ErrRelativePathNotSupported Error = "relative path not supported"
	// ErrMissingHost is the missing host error.
	ErrMissingHost Error = "missing host"
	// ErrMissingPath is the missing path error.
	ErrMissingPath Error = "missing path"
	// ErrMissingUser is the missing user error.
	ErrMissingUser Error = "missing user"
)
