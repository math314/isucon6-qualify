package main

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"log"
	"math"
	"mdb"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/Songmu/strrand"
	_ "github.com/go-sql-driver/mysql"
	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	"github.com/unrolled/render"

	_ "net/http/pprof"
)

type Entry struct {
	ID          int
	AuthorID    int
	Keyword     string
	Description string
	UpdatedAt   time.Time
	CreatedAt   time.Time

	Html  string
	Stars []*mdb.Star
}

type User struct {
	ID        int
	Name      string
	Salt      string
	Password  string
	CreatedAt time.Time
}

type EntryWithCtx struct {
	Context context.Context
	Entry   Entry
}


func prepareHandler(fn func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			baseUrl, _ = url.Parse("http://" + h)
		} else {
			baseUrl, _ = url.Parse("http://" + r.Host)
		}
		fn(w, r)
	}
}

func myHandler(fn func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				fmt.Fprintf(os.Stderr, "%+v", err)
				debug.PrintStack()
				http.Error(w, http.StatusText(500), 500)
			}
		}()
		prepareHandler(fn)(w, r)
	}
}

func pathURIEscape(s string) string {
	return (&url.URL{Path: s}).String()
}

func notFound(w http.ResponseWriter) {
	code := http.StatusNotFound
	http.Error(w, http.StatusText(code), code)
}

func badRequest(w http.ResponseWriter) {
	code := http.StatusBadRequest
	http.Error(w, http.StatusText(code), code)
}

func forbidden(w http.ResponseWriter) {
	code := http.StatusForbidden
	http.Error(w, http.StatusText(code), code)
}

func panicIf(err error) {
	if err != nil {
		panic(err)
	}
}

const (
	sessionName   = "isuda_session"
	sessionSecret = "tonymoris"
)

var (
	isupamEndpoint string

	baseUrl *url.URL
	db      *sql.DB
	re      *render.Render
	store   *sessions.CookieStore

	errInvalidUser = errors.New("Invalid User")

	entryStore *mdb.EntryStore
	starStore *mdb.StarStore
)

func setName(w http.ResponseWriter, r *http.Request) error {
	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
	if !ok {
		return nil
	}
	setContext(r, "user_id", userID)
	row := db.QueryRow(`SELECT name FROM user WHERE id = ?`, userID)
	user := User{}
	err := row.Scan(&user.Name)
	if err != nil {
		if err == sql.ErrNoRows {
			return errInvalidUser
		}
		panicIf(err)
	}
	setContext(r, "user_name", user.Name)
	return nil
}

func authenticate(w http.ResponseWriter, r *http.Request) error {
	if u := getContext(r, "user_id"); u != nil {
		return nil
	}
	return errInvalidUser
}

func initializeHandler(w http.ResponseWriter, r *http.Request) {
	_, err := db.Exec(`DELETE FROM entry WHERE id > 7101`)
	panicIf(err)
	if entryStore != nil {
		entryStore.Close()
	}
	entryStore = mdb.NewEntryStore(db)

	_, err = db.Exec("TRUNCATE star")
	panicIf(err)
	if starStore != nil {
		starStore.Close()
	}
	starStore = mdb.NewStarStore(db)

	re.JSON(w, http.StatusOK, map[string]string{"result": "ok"})
}


func starsHandler(w http.ResponseWriter, r *http.Request) {
	keyword := r.FormValue("keyword")
	stars := loadStars(keyword)

	re.JSON(w, http.StatusOK, map[string][]*mdb.Star{
		"result": stars,
	})
}

func starsPostHandler(w http.ResponseWriter, r *http.Request) {
	keyword := r.FormValue("keyword")

	exists := entryStore.KeywordExists(keyword)
	if !exists {
		notFound(w)
		return
	}

	user := r.FormValue("user")
	starStore.Insert(keyword, user)

	re.JSON(w, http.StatusOK, map[string]string{"result": "ok"})
}

func topHandler(w http.ResponseWriter, r *http.Request) {
	if err := setName(w, r); err != nil {
		forbidden(w)
		return
	}

	perPage := 10
	p := r.URL.Query().Get("page")
	if p == "" {
		p = "1"
	}
	page, _ := strconv.Atoi(p)

	oriEntries := entryStore.SelectTopNSkipMOrderByUpdated(perPage, perPage*(page-1))
	entries := make([]*Entry, len(oriEntries))

	x := loadReplacer()
	for i, e := range oriEntries {
		entries[i] = &Entry{
			ID: e.ID,
			Keyword: e.Keyword,
			Description: e.Description,
			UpdatedAt: e.UpdatedAt,
			AuthorID: e.AuthorID,
			CreatedAt: e.CreatedAt,
		}
		entries[i].Html = htmlify(w, r, x, e.Description)
		entries[i].Stars = loadStars(e.Keyword)
	}

	totalEntries := entryStore.TotalCount()
	lastPage := int(math.Ceil(float64(totalEntries) / float64(perPage)))
	pages := make([]int, 0, 10)
	start := int(math.Max(float64(1), float64(page-5)))
	end := int(math.Min(float64(lastPage), float64(page+5)))
	for i := start; i <= end; i++ {
		pages = append(pages, i)
	}

	re.HTML(w, http.StatusOK, "index", struct {
		Context  context.Context
		Entries  []*Entry
		Page     int
		LastPage int
		Pages    []int
	}{
		r.Context(), entries, page, lastPage, pages,
	})
}

func robotsHandler(w http.ResponseWriter, r *http.Request) {
	notFound(w)
}

func keywordPostHandler(w http.ResponseWriter, r *http.Request) {
	if err := setName(w, r); err != nil {
		forbidden(w)
		return
	}
	if err := authenticate(w, r); err != nil {
		forbidden(w)
		return
	}

	keyword := r.FormValue("keyword")
	if keyword == "" {
		badRequest(w)
		return
	}
	userID := getContext(r, "user_id").(int)
	description := r.FormValue("description")

	if isSpamContents(description) || isSpamContents(keyword) {
		http.Error(w, "SPAM!", http.StatusBadRequest)
		return
	}
	err := entryStore.UpdateOrInsertWithKeyword(keyword, userID, description)
	panicIf(err)
	http.Redirect(w, r, "/", http.StatusFound)
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if err := setName(w, r); err != nil {
		forbidden(w)
		return
	}

	re.HTML(w, http.StatusOK, "authenticate", struct {
		Context context.Context
		Action  string
	}{
		r.Context(), "login",
	})
}

func loginPostHandler(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	row := db.QueryRow(`SELECT * FROM user WHERE name = ?`, name)
	user := User{}
	err := row.Scan(&user.ID, &user.Name, &user.Salt, &user.Password, &user.CreatedAt)
	if err == sql.ErrNoRows || user.Password != fmt.Sprintf("%x", sha1.Sum([]byte(user.Salt+r.FormValue("password")))) {
		forbidden(w)
		return
	}
	panicIf(err)
	session := getSession(w, r)
	session.Values["user_id"] = user.ID
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	session := getSession(w, r)
	session.Options = &sessions.Options{MaxAge: -1}
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func registerHandler(w http.ResponseWriter, r *http.Request) {
	if err := setName(w, r); err != nil {
		forbidden(w)
		return
	}

	re.HTML(w, http.StatusOK, "authenticate", struct {
		Context context.Context
		Action  string
	}{
		r.Context(), "register",
	})
}

func registerPostHandler(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	pw := r.FormValue("password")
	if name == "" || pw == "" {
		badRequest(w)
		return
	}
	userID := register(name, pw)
	session := getSession(w, r)
	session.Values["user_id"] = userID
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func register(user string, pass string) int64 {
	salt, err := strrand.RandomString(`....................`)
	panicIf(err)
	res, err := db.Exec(`INSERT INTO user (name, salt, password, created_at) VALUES (?, ?, ?, NOW())`,
		user, salt, fmt.Sprintf("%x", sha1.Sum([]byte(salt+pass))))
	panicIf(err)
	lastInsertID, _ := res.LastInsertId()
	return lastInsertID
}

func keywordByKeywordHandler(w http.ResponseWriter, r *http.Request) {
	if err := setName(w, r); err != nil {
		forbidden(w)
		return
	}

	keyword, err := url.QueryUnescape(mux.Vars(r)["keyword"])
	panicIf(err)

	orie, error := entryStore.SelectWithKeyword(keyword)
	if error != nil {
		notFound(w)
		return
	}

	x := loadReplacer()

	e := Entry{
		ID: orie.ID,
		Keyword: orie.Keyword,
		Description: orie.Description,
		UpdatedAt: orie.UpdatedAt,
		AuthorID: orie.AuthorID,
		CreatedAt: orie.CreatedAt,
	}
	e.Html = htmlify(w, r, x, e.Description)
	e.Stars = loadStars(e.Keyword)

	re.HTML(w, http.StatusOK, "keyword", struct {
		Context context.Context
		Entry   Entry
	}{
		r.Context(), e,
	})
}

func keywordByKeywordDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if err := setName(w, r); err != nil {
		forbidden(w)
		return
	}
	if err := authenticate(w, r); err != nil {
		forbidden(w)
		return
	}

	keyword, err := url.QueryUnescape(mux.Vars(r)["keyword"])
	panicIf(err)
	if keyword == "" {
		badRequest(w)
		return
	}
	if r.FormValue("delete") == "" {
		badRequest(w)
		return
	}

	exists := entryStore.KeywordExists(keyword)
	if !exists {
		notFound(w)
		return
	}
	err = entryStore.DeleteWithKeyword(keyword)
	panicIf(err)
	http.Redirect(w, r, "/", http.StatusFound)
}

type Replacer struct {
	r *strings.Replacer
	lastUpdated int64
}

var cachedReplacer *Replacer

func loadReplacer() *Replacer {
	cacheLastUpdated := int64(0)
	if cachedReplacer != nil {
		cacheLastUpdated = cachedReplacer.lastUpdated
	}

	lastUpdated := entryStore.KeywordLastUpdated()
	if cacheLastUpdated >= lastUpdated {
		return cachedReplacer
	}

	keywords := entryStore.GetAllKeywordsOrderByLength()
	keywordsWithReplace := make([]string, 0, len(keywords) * 2)
	for _, k := range keywords {
		u := baseUrl.String()+"/keyword/" + pathURIEscape(k)
		link := fmt.Sprintf("<a href=\"%s\">%s</a>", u, html.EscapeString(k))
		keywordsWithReplace = append(keywordsWithReplace, k)
		keywordsWithReplace = append(keywordsWithReplace, link)
	}

	cachedReplacer = &Replacer{
		strings.NewReplacer(keywordsWithReplace...), lastUpdated,
	}
	return cachedReplacer
}

func htmlify(w http.ResponseWriter, r *http.Request, x *Replacer, content string) string {
	if content == "" {
		return ""
	}
	content = x.r.Replace(content)
	return strings.Replace(content, "\n", "<br />\n", -1)
}

func loadStars(keyword string) []*mdb.Star {
	return starStore.SelectWithKeyword(keyword)
}

func isSpamContents(content string) bool {
	if os.Getenv("LOCAL") == "1" {
		return false
	}

	v := url.Values{}
	v.Set("content", content)
	resp, err := http.PostForm(isupamEndpoint, v)
	panicIf(err)
	defer resp.Body.Close()

	var data struct {
		Valid bool `json:valid`
	}
	err = json.NewDecoder(resp.Body).Decode(&data)
	panicIf(err)
	return !data.Valid
}

func getContext(r *http.Request, key interface{}) interface{} {
	return r.Context().Value(key)
}

func setContext(r *http.Request, key, val interface{}) {
	if val == nil {
		return
	}

	r2 := r.WithContext(context.WithValue(r.Context(), key, val))
	*r = *r2
}

func getSession(w http.ResponseWriter, r *http.Request) *sessions.Session {
	session, _ := store.Get(r, sessionName)
	return session
}

func main() {
	if os.Getenv("DISABLE_PROF") != "1" {
		go func() {
			log.Println(http.ListenAndServe(":6060", nil))
		}()
	}

	host := os.Getenv("ISUDA_DB_HOST")
	if host == "" {
		host = "localhost"
	}
	portstr := os.Getenv("ISUDA_DB_PORT")
	if portstr == "" {
		portstr = "3306"
	}
	port, err := strconv.Atoi(portstr)
	if err != nil {
		log.Fatalf("Failed to read DB port number from an environment variable ISUDA_DB_PORT.\nError: %s", err.Error())
	}
	user := os.Getenv("ISUDA_DB_USER")
	if user == "" {
		user = "root"
	}
	password := os.Getenv("ISUDA_DB_PASSWORD")
	dbname := os.Getenv("ISUDA_DB_NAME")
	if dbname == "" {
		dbname = "isuda"
	}

	connectionStr := fmt.Sprintf(
		"%s:%s@tcp(%s:%d)/%s?loc=Local&parseTime=true",
		user, password, host, port, dbname,
	)
	log.Print(connectionStr)

	db, err = sql.Open("mysql", connectionStr)
	if err != nil {
		log.Fatalf("Failed to connect to DB: %s.", err.Error())
	}
	db.Exec("SET SESSION sql_mode='TRADITIONAL,NO_AUTO_VALUE_ON_ZERO,ONLY_FULL_GROUP_BY'")
	db.Exec("SET NAMES utf8mb4")

	entryStore = mdb.NewEntryStore(db)
	starStore = mdb.NewStarStore(db)

	isupamEndpoint = os.Getenv("ISUPAM_ORIGIN")
	if isupamEndpoint == "" {
		isupamEndpoint = "http://localhost:5050"
	}

	store = sessions.NewCookieStore([]byte(sessionSecret))

	viewDir := os.Getenv("viewDir")
	if viewDir == "" {
		viewDir= "views"
	}

	re = render.New(render.Options{
		Directory: viewDir,
		Funcs: []template.FuncMap{
			{
				"url_for": func(path string) string {
					return baseUrl.String() + path
				},
				"title": func(s string) string {
					return strings.Title(s)
				},
				"raw": func(text string) template.HTML {
					return template.HTML(text)
				},
				"add": func(a, b int) int { return a + b },
				"sub": func(a, b int) int { return a - b },
				"entry_with_ctx": func(entry Entry, ctx context.Context) *EntryWithCtx {
					return &EntryWithCtx{Context: ctx, Entry: entry}
				},
			},
		},
	})

	r := mux.NewRouter()
	r.UseEncodedPath()
	r.HandleFunc("/", myHandler(topHandler))
	r.HandleFunc("/initialize", myHandler(initializeHandler)).Methods("GET")
	r.HandleFunc("/robots.txt", myHandler(robotsHandler))
	r.HandleFunc("/keyword", myHandler(keywordPostHandler)).Methods("POST")

	l := r.PathPrefix("/login").Subrouter()
	l.Methods("GET").HandlerFunc(myHandler(loginHandler))
	l.Methods("POST").HandlerFunc(myHandler(loginPostHandler))
	r.HandleFunc("/logout", myHandler(logoutHandler))

	g := r.PathPrefix("/register").Subrouter()
	g.Methods("GET").HandlerFunc(myHandler(registerHandler))
	g.Methods("POST").HandlerFunc(myHandler(registerPostHandler))

	k := r.PathPrefix("/keyword/{keyword}").Subrouter()
	k.Methods("GET").HandlerFunc(myHandler(keywordByKeywordHandler))
	k.Methods("POST").HandlerFunc(myHandler(keywordByKeywordDeleteHandler))

	s := r.PathPrefix("/stars").Subrouter()
	s.Methods("GET").HandlerFunc(myHandler(starsHandler))
	s.Methods("POST").HandlerFunc(myHandler(starsPostHandler))

	r.PathPrefix("/").Handler(http.FileServer(http.Dir("./public/")))

	loggingMiddleware := func (next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Do stuff here
			log.Println(r.RequestURI)
			// Call the next handler, which can be another middleware in the chain, or the final handler.
			next.ServeHTTP(w, r)
		})
	}
	r.Use(loggingMiddleware)

	log.Fatal(http.ListenAndServe(":5000", r))
}
