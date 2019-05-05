package mdb

import (
	"database/sql"
	"log"
	"sync"
	"time"
)

type Star struct {
	ID        int       `json:"id"`
	Keyword   string    `json:"keyword"`
	UserName  string    `json:"user_name"`
	CreatedAt time.Time `json:"created_at"`
}

type StringIndex struct {
	valToPKs map[string][]int
}

func NewStringIndex() *StringIndex {
	return &StringIndex{map[string][]int{}}
}

func (s* StringIndex) Insert(val string, pk int) {
	s.valToPKs[val] = append(s.valToPKs[val], pk)
}

type StarStore struct {
	sync.RWMutex
	db      *sql.DB
	store   []*Star
	deleted []bool

	keywordIndex *StringIndex

	dbOpChan chan <- DBOperatorEntry
}

var EMPTY_Star = &Star{}

func NewStarStore(db *sql.DB) *StarStore {
	store := make([]*Star, 0, 10000)
	deleted := make([]bool, 0, 10000)

	deleted = append(deleted, true)
	// id = 0 is unavailable
	store = append(store, EMPTY_Star)

	rows, err := db.Query(`SELECT * FROM star`)
	if err != nil {
		log.Fatal(err)
	}

	keywordIndex := NewStringIndex()
	for rows.Next() {

		s := &Star{}
		err := rows.Scan(&s.ID, &s.Keyword, &s.UserName, &s.CreatedAt)
		if err != nil {
			log.Fatal(err)
		}
		for s.ID >= len(store) {
			store = append(store, EMPTY_Star)
			deleted = append(deleted, true)
		}
		store[s.ID] = s
		deleted[s.ID] = false

		keywordIndex.Insert(s.Keyword, s.ID)
	}
	rows.Close()

	dbOpChan := make(chan DBOperatorEntry, 100)

	startDBOpfunc(db, dbOpChan)

	return &StarStore{
		RWMutex: sync.RWMutex{},
		db:      db,
		store:   store,
		deleted: deleted,
		keywordIndex: keywordIndex,
		dbOpChan: dbOpChan,
	}
}

func (s* StarStore) Close() {
	close(s.dbOpChan)
}

func (s* StarStore) Insert(keyword, userName string) {
	s.Lock()
	defer s.Unlock()

	now := time.Now()

	star := Star{}
	star.ID = len(s.store)
	star.CreatedAt = now
	star.Keyword = keyword
	star.UserName = userName

	s.store = append(s.store, &star)
	s.deleted = append(s.deleted, false)
	s.keywordIndex.Insert(keyword, star.ID)

	s.dbOpChan <- DBOperatorEntry{
		`INSERT INTO star (keyword, user_name, created_at) VALUES (?, ?, ?)`, []interface{}{keyword, userName, now},
	}
}

func (s* StarStore) SelectWithKeyword(keyword string) []*Star {
	s.RLock()
	defer s.RUnlock()

	ret := make([]*Star, 0, 10)
	for _, id := range s.keywordIndex.valToPKs[keyword] {
		tmp := *s.store[id]
		ret = append(ret, &tmp)
	}

	return ret
}