package pim

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
)

const davNS = "DAV:"

type DAVClient struct {
	http                       *http.Client
	base                       *url.URL
	username, secret, protocol string
}
type davProps struct {
	ETag     string `xml:"DAV: getetag"`
	Name     string `xml:"DAV: displayname"`
	Resource struct {
		Collection  *struct{} `xml:"DAV: collection"`
		AddressBook *struct{} `xml:"urn:ietf:params:xml:ns:carddav addressbook"`
		Calendar    *struct{} `xml:"urn:ietf:params:xml:ns:caldav calendar"`
	} `xml:"DAV: resourcetype"`
	Principal struct {
		Href string `xml:"DAV: href"`
	} `xml:"DAV: current-user-principal"`
	AddressHome struct {
		Href string `xml:"DAV: href"`
	} `xml:"urn:ietf:params:xml:ns:carddav addressbook-home-set"`
	CalendarHome struct {
		Href string `xml:"DAV: href"`
	} `xml:"urn:ietf:params:xml:ns:caldav calendar-home-set"`
}
type davResponse struct {
	Href   string `xml:"DAV: href"`
	Status string `xml:"DAV: status"`
	Props  []struct {
		Status string   `xml:"DAV: status"`
		Value  davProps `xml:"DAV: prop"`
	} `xml:"DAV: propstat"`
}
type multiStatus struct {
	XMLName   xml.Name      `xml:"DAV: multistatus"`
	Responses []davResponse `xml:"DAV: response"`
}

func NewDAV(c config.Collection, secret string) (*DAVClient, error) {
	u, err := url.Parse(c.Remote)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("DAV requires HTTPS")
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
		if u.RawPath != "" {
			u.RawPath += "/"
		}
	}
	d := &DAVClient{base: u, username: c.DAV.Username, secret: secret, protocol: c.Protocol}
	d.http = &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Host, u.Host) {
			return errors.New("refusing DAV redirect outside authenticated HTTPS origin")
		}
		if len(via) >= 10 {
			return errors.New("too many DAV redirects")
		}
		return nil
	}}
	return d, nil
}

func (d *DAVClient) resolve(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	u = d.base.ResolveReference(u)
	if u.Scheme != "https" || !strings.EqualFold(u.Host, d.base.Host) || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("DAV href is outside authenticated origin or contains a query/fragment")
	}
	return u, nil
}
func (d *DAVClient) itemURL(raw string) (string, error) {
	u, err := d.resolve(raw)
	if err != nil {
		return "", err
	}
	rel := strings.TrimPrefix(u.Path, d.base.Path)
	if !strings.HasPrefix(u.Path, d.base.Path) || rel == "" || strings.Contains(rel, "/") || rel == "." || rel == ".." {
		return "", errors.New("DAV resource is outside mapped collection")
	}
	return u.String(), nil
}

func (d *DAVClient) request(ctx context.Context, method, endpoint string, data []byte, headers map[string]string) ([]byte, http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	req.SetBasicAuth(d.username, d.secret)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("DAV %s: HTTP %s (412 means a concurrent edit; rerun sync)", method, resp.Status)
	}
	const maxSize = 64 << 20
	data, err = io.ReadAll(io.LimitReader(resp.Body, maxSize+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > maxSize {
		return nil, nil, errors.New("DAV response exceeds 64 MiB")
	}
	if method == "PROPFIND" && resp.StatusCode != http.StatusMultiStatus {
		return nil, nil, errors.New("DAV PROPFIND did not return a complete multistatus")
	}
	return data, resp.Header, nil
}

const propfind = `<d:propfind xmlns:d="DAV:" xmlns:a="urn:ietf:params:xml:ns:carddav" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><d:getetag/><d:resourcetype/><d:displayname/><d:current-user-principal/><a:addressbook-home-set/><c:calendar-home-set/></d:prop></d:propfind>`

func (d *DAVClient) list(ctx context.Context, endpoint, depth string) ([]davResponse, error) {
	data, _, err := d.request(ctx, "PROPFIND", endpoint, []byte(propfind), map[string]string{"Depth": depth, "Content-Type": "application/xml; charset=utf-8"})
	if err != nil {
		return nil, err
	}
	var m multiStatus
	if err := xml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if len(m.Responses) == 0 {
		return nil, errors.New("empty DAV multistatus; refusing to infer deletions")
	}
	for _, r := range m.Responses {
		if r.Href == "" {
			return nil, errors.New("DAV response has no href")
		}
		if r.Status != "" && !statusOK(r.Status) {
			return nil, fmt.Errorf("DAV listing failed for %s: %s", r.Href, r.Status)
		}
		if len(r.Props) == 0 {
			return nil, errors.New("DAV response has no properties")
		}
	}
	return m.Responses, nil
}
func statusOK(s string) bool {
	fields := strings.Fields(s)
	return len(fields) >= 2 && fields[1] == "200"
}

func (d *DAVClient) get(ctx context.Context, id string) (Object, error) {
	u, err := d.itemURL(id)
	if err != nil {
		return Object{}, err
	}
	data, headers, err := d.request(ctx, "GET", u, nil, nil)
	if err != nil {
		return Object{}, err
	}
	rev := headers.Get("ETag")
	if rev == "" || strings.HasPrefix(rev, "W/") {
		return Object{}, errors.New("DAV requires a strong ETag for safe synchronization")
	}
	return Object{ID: u, Revision: rev, Data: data}, nil
}

func (d *DAVClient) Snapshot(ctx context.Context) ([]Object, error) {
	rows, err := d.list(ctx, d.base.String(), "1")
	if err != nil {
		return nil, err
	}
	objects := []Object{}
	sawCollection := false
	for _, r := range rows {
		u, err := d.resolve(r.Href)
		if err != nil {
			return nil, err
		}
		isSelf := strings.TrimRight(u.Path, "/") == strings.TrimRight(d.base.Path, "/")
		var etag string
		collection := false
		kind := false
		for _, p := range r.Props {
			if !statusOK(p.Status) {
				continue
			}
			if p.Value.ETag != "" {
				etag = p.Value.ETag
			}
			if p.Value.Resource.Collection != nil {
				collection = true
			}
			if (d.protocol == "carddav" && p.Value.Resource.AddressBook != nil) || (d.protocol == "caldav" && p.Value.Resource.Calendar != nil) {
				kind = true
			}
		}
		if isSelf {
			if !kind {
				return nil, errors.New("remote URL is not the requested DAV address book/calendar; use discover to find a collection URL")
			}
			sawCollection = true
			continue
		}
		if collection {
			return nil, errors.New("mapped DAV URL contains nested collections; map each collection separately")
		}
		if etag == "" {
			return nil, fmt.Errorf("DAV listing lacks ETag for %s", r.Href)
		}
		o, err := d.get(ctx, u.String())
		if err != nil {
			return nil, err
		}
		if o.Revision != etag {
			return nil, errors.New("DAV object changed while listing; rerun sync")
		}
		objects = append(objects, o)
	}
	if !sawCollection {
		return nil, errors.New("DAV listing omitted mapped collection; refusing to infer deletions")
	}
	return objects, nil
}

func (d *DAVClient) Put(ctx context.Context, id, revision string, data []byte) (Object, error) {
	uid, err := UID(data, Extension(d.protocol))
	if err != nil {
		return Object{}, err
	}
	if id == "" {
		id = d.base.ResolveReference(&url.URL{Path: digest([]byte(uid)) + Extension(d.protocol)}).String()
	}
	u, err := d.itemURL(id)
	if err != nil {
		return Object{}, err
	}
	typeName := "text/vcard; charset=utf-8"
	if d.protocol == "caldav" {
		typeName = "text/calendar; charset=utf-8"
	}
	headers := map[string]string{"Content-Type": typeName}
	if revision == "" {
		headers["If-None-Match"] = "*"
	} else {
		headers["If-Match"] = revision
	}
	_, responseHeaders, err := d.request(ctx, "PUT", u, data, headers)
	if err != nil {
		return Object{}, err
	}
	o, err := d.get(ctx, u)
	if err == nil && responseHeaders.Get("ETag") != "" && responseHeaders.Get("ETag") != o.Revision {
		return Object{}, errors.New("DAV item changed immediately after write; rerun sync")
	}
	return o, err
}
func (d *DAVClient) Delete(ctx context.Context, o Object) error {
	u, err := d.itemURL(o.ID)
	if err != nil {
		return err
	}
	_, _, err = d.request(ctx, "DELETE", u, nil, map[string]string{"If-Match": o.Revision})
	return err
}

type Discovered struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Discover accepts a server/principal/home/collection URL. It follows DAV
// principal and home-set properties only within the authenticated origin.
func (d *DAVClient) Discover(ctx context.Context) ([]Discovered, error) {
	endpoint := d.base.String()
	seen := map[string]bool{}
	for step := 0; step < 5; step++ {
		if seen[endpoint] {
			return nil, errors.New("DAV discovery loop")
		}
		seen[endpoint] = true
		rows, err := d.list(ctx, endpoint, "1")
		if err != nil {
			return nil, err
		}
		out := []Discovered{}
		next := ""
		principal := ""
		for _, r := range rows {
			for _, p := range r.Props {
				if !statusOK(p.Status) {
					continue
				}
				v := p.Value
				if (d.protocol == "carddav" && v.Resource.AddressBook != nil) || (d.protocol == "caldav" && v.Resource.Calendar != nil) {
					u, err := d.resolve(r.Href)
					if err != nil {
						return nil, err
					}
					out = append(out, Discovered{ID: u.String(), Name: v.Name})
				}
				if d.protocol == "carddav" && v.AddressHome.Href != "" {
					next = v.AddressHome.Href
				}
				if d.protocol == "caldav" && v.CalendarHome.Href != "" {
					next = v.CalendarHome.Href
				}
				if v.Principal.Href != "" {
					principal = v.Principal.Href
				}
			}
		}
		if len(out) > 0 {
			return out, nil
		}
		if next == "" {
			next = principal
		}
		if next == "" {
			return nil, errors.New("DAV server supplied no collections or discovery properties; configure the collection URL directly")
		}
		u, err := d.resolve(next)
		if err != nil {
			return nil, err
		}
		endpoint = u.String()
	}
	return nil, errors.New("DAV discovery exceeded five steps")
}
