package pim

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.cerberusgames.ca/Starstreak/MailSalonSync/internal/config"
)

func xmlText(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}
func TestDAVRoundTripAndConditions(t *testing.T) {
	for _, protocol := range []string{"carddav", "caldav"} {
		t.Run(protocol, func(t *testing.T) {
			resource := "addressbook"
			ns := "urn:ietf:params:xml:ns:carddav"
			ext := ".vcf"
			original := card("u1", "Alice")
			if protocol == "caldav" {
				resource = "calendar"
				ns = "urn:ietf:params:xml:ns:caldav"
				ext = ".ics"
				original = []byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:u1\r\nSUMMARY:Appointment\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
			}
			items := map[string][]byte{"/book/a" + ext: original}
			etag := `"1"`
			puts, deletes := 0, 0
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, password, ok := r.BasicAuth()
				if !ok || user != "user" || password != "secret" {
					t.Error("missing credentials")
					w.WriteHeader(401)
					return
				}
				switch r.Method {
				case "PROPFIND":
					if r.Header.Get("Depth") != "1" {
						t.Error("wrong depth")
					}
					w.WriteHeader(207)
					fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:p="%s"><d:response><d:href>/book/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/><p:%s/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, ns, resource)
					for path := range items {
						fmt.Fprintf(w, `<d:response><d:href>%s</d:href><d:propstat><d:prop><d:getetag>%s</d:getetag><d:resourcetype/></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`, path, xmlText(etag))
					}
					fmt.Fprint(w, `</d:multistatus>`)
				case "GET":
					data, ok := items[r.URL.Path]
					if !ok {
						w.WriteHeader(404)
						return
					}
					w.Header().Set("ETag", etag)
					w.Write(data)
				case "PUT":
					_, exists := items[r.URL.Path]
					if exists && r.Header.Get("If-Match") != etag {
						t.Error("update lacks If-Match")
						w.WriteHeader(412)
						return
					}
					if !exists && r.Header.Get("If-None-Match") != "*" {
						t.Error("create lacks If-None-Match")
						w.WriteHeader(412)
						return
					}
					if !strings.Contains(r.Header.Get("Content-Type"), "text/") {
						t.Error("missing content type")
					}
					data := make([]byte, r.ContentLength)
					_, err := r.Body.Read(data)
					_ = err
					items[r.URL.Path] = data
					etag = `"2"`
					puts++
					w.Header().Set("ETag", etag)
					w.WriteHeader(201)
				case "DELETE":
					if r.Header.Get("If-Match") != etag {
						t.Error("delete lacks If-Match")
						w.WriteHeader(412)
						return
					}
					delete(items, r.URL.Path)
					deletes++
					w.WriteHeader(204)
				}
			}))
			defer srv.Close()
			c := config.Collection{Protocol: protocol, Remote: srv.URL + "/book/", DAV: &config.DAV{Username: "user"}}
			d, err := NewDAV(c, "secret")
			if err != nil {
				t.Fatal(err)
			}
			d.http.Transport = srv.Client().Transport
			ctx := context.Background()
			objects, err := d.Snapshot(ctx)
			if err != nil || len(objects) != 1 {
				t.Fatalf("snapshot: %v %+v", err, objects)
			}
			updated, err := d.Put(ctx, objects[0].ID, objects[0].Revision, original)
			if err != nil {
				t.Fatal(err)
			}
			created, err := d.Put(ctx, "", "", original)
			if err != nil {
				t.Fatal(err)
			}
			if err := d.Delete(ctx, created); err != nil {
				t.Fatal(err)
			}
			if puts != 2 || deletes != 1 || updated.Revision != `"2"` {
				t.Fatal("missing mutations")
			}
			if _, err := d.itemURL("https://other.test/book/a"); err == nil {
				t.Fatal("accepted foreign origin")
			}
			if _, err := d.itemURL("../outside"); err == nil {
				t.Fatal("accepted outside path")
			}
		})
	}
}
func TestDAVRefusesIncompleteListing(t *testing.T) {
	for _, body := range []string{`<d:multistatus xmlns:d="DAV:"></d:multistatus>`, `<d:multistatus xmlns:d="DAV:"><d:response><d:href>/book/a.vcf</d:href><d:propstat><d:prop/><d:status>HTTP/1.1 403 Forbidden</d:status></d:propstat></d:response></d:multistatus>`, `<d:multistatus xmlns:d="DAV:">broken`} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(207); fmt.Fprint(w, body) }))
		d, _ := NewDAV(config.Collection{Protocol: "carddav", Remote: srv.URL + "/book/", DAV: &config.DAV{Username: "u"}}, "s")
		d.http.Transport = srv.Client().Transport
		if _, err := d.Snapshot(context.Background()); err == nil {
			t.Fatalf("accepted partial listing: %s", body)
		}
		srv.Close()
	}
}
func TestDAVDiscovery(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(207)
		if r.URL.Path == "/" {
			fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:" xmlns:a="urn:ietf:params:xml:ns:carddav"><d:response><d:href>/</d:href><d:propstat><d:prop><a:addressbook-home-set><d:href>/home/</d:href></a:addressbook-home-set></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
		} else {
			fmt.Fprint(w, `<d:multistatus xmlns:d="DAV:" xmlns:a="urn:ietf:params:xml:ns:carddav"><d:response><d:href>/home/personal/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/><a:addressbook/></d:resourcetype><d:displayname>Personal</d:displayname></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
		}
	}))
	defer srv.Close()
	d, _ := NewDAV(config.Collection{Protocol: "carddav", Remote: srv.URL + "/", DAV: &config.DAV{Username: "u"}}, "s")
	d.http.Transport = srv.Client().Transport
	rows, err := d.Discover(context.Background())
	if err != nil || len(rows) != 1 || rows[0].Name != "Personal" || rows[0].ID != srv.URL+"/home/personal/" {
		t.Fatalf("discovery: %+v %v", rows, err)
	}
}
