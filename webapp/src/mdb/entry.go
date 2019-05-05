package mdb

import (
	"database/sql"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

// *** thread unsafe ***
type StringUniqueIndex struct {
	valToPK map[string]int
	lastUpdated int64
}

func NewStringUniqueIndex() *StringUniqueIndex {
	return &StringUniqueIndex{
		map[string]int{}, time.Now().Unix(),
	}
}

func (s* StringUniqueIndex) Find(val string) (int, bool) {
	ret, found := s.valToPK[val]
	return ret, found
}

func (s* StringUniqueIndex) Insert(val string, pk int) error {
	if _, found := s.valToPK[val]; found {
		return fmt.Errorf("%s already inserted", val)
	}
	s.valToPK[val] = pk
	s.lastUpdated = time.Now().Unix()
	return nil
}

func (s* StringUniqueIndex) Delete(val string) error {
	if _, found := s.valToPK[val]; !found {
		return fmt.Errorf("%s does not exist", val)
	}
	delete(s.valToPK, val)
	s.lastUpdated = time.Now().Unix()
	return nil
}

type Entry struct {
	ID          int
	AuthorID    int
	Keyword     string
	Description string
	UpdatedAt   time.Time
	CreatedAt   time.Time
}

type EntryStore struct {
	sync.RWMutex
	db      *sql.DB
	store   []*Entry
	deleted []bool

	count int
	keywordIndex *StringUniqueIndex

	dbOpChan chan <- DBOperatorEntry
}

type DBOperatorEntry struct {
	query string
	args []interface{}
}

var EMPTY_ENTRY = &Entry{}

func startDBOpfunc(db *sql.DB, dbOps <- chan DBOperatorEntry) {
	go func() {
		for val := range dbOps {
			log.Printf("query = %s, args = %v", val.query, val.args)
			_, err := db.Exec(val.query, val.args...)
			if err != nil {
				log.Print(err)
			}
		}
	}()
}

func NewEntryStore(db *sql.DB) *EntryStore {
	store := make([]*Entry, 0, 10000)
	deleted := make([]bool, 0, 10000)

	// id = 0 is unavailable
	store = append(store, EMPTY_ENTRY)
	deleted = append(deleted, true)

	rows, err := db.Query(`SELECT * FROM entry`)
	if err != nil {
		log.Fatal(err)
	}

	count := 0
	keywordIndex := NewStringUniqueIndex()
	for rows.Next() {
		count++

		e := Entry{}
		err := rows.Scan(&e.ID, &e.AuthorID, &e.Keyword, &e.Description, &e.UpdatedAt, &e.CreatedAt)
		if err != nil {
			log.Fatal(err)
		}
		for e.ID >= len(store) {
			store = append(store, EMPTY_ENTRY)
			deleted = append(deleted, true)
		}
		store[e.ID] = &e
		deleted[e.ID] = false

		keywordIndex.Insert(e.Keyword, e.ID)
	}
	rows.Close()

	dbOpChan := make(chan DBOperatorEntry, 100)

	startDBOpfunc(db, dbOpChan)

	return &EntryStore{
		RWMutex: sync.RWMutex{},
		db:      db,
		store:   store,
		deleted: deleted,
		count: count,
		keywordIndex: keywordIndex,
		dbOpChan: dbOpChan,
	}
}

func (e* EntryStore) Close() {
	close(e.dbOpChan)
}

func (e* EntryStore) insertWithoutLock(keyword string, authorID int, description string) error {
	debugId, found := e.keywordIndex.Find(keyword)
	if found {
		return fmt.Errorf("%s is already used by id = %d", keyword, debugId)
	}

	entry := Entry{}
	entry.ID = len(e.store)
	entry.CreatedAt = time.Now()
	entry.UpdatedAt = entry.CreatedAt
	entry.Keyword = keyword
	entry.AuthorID = authorID
	entry.Description = description

	e.store = append(e.store, &entry)
	e.deleted = append(e.deleted, false)
	e.count++
	e.keywordIndex.Insert(entry.Keyword, entry.ID)
	e.dbOpChan <- DBOperatorEntry{`INSERT INTO entry (author_id, keyword, description, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`,  []interface{}{
			entry.AuthorID, entry.Keyword, entry.Description, entry.CreatedAt, entry.UpdatedAt,
	}}

	return nil
}

func (e* EntryStore) updateWithKeyword(keyword string, authorID int, description string) error {
	id, found := e.keywordIndex.Find(keyword)
	if !found {
		return fmt.Errorf("%s is not used", keyword)
	}

	if e.deleted[id] {
		return fmt.Errorf("%s is already deleted. Index is broken", id)
	}

	tmp := e.store[id]

	tmp.AuthorID = authorID
	tmp.UpdatedAt = time.Now()
	tmp.Description = description

	e.dbOpChan <- DBOperatorEntry{`UPDATE entry SET author_id = ?, keyword = ?, description = ?, updated_at = ? WHERE keyword = ?`, []interface{}{
		tmp.AuthorID, tmp.Keyword, tmp.Description, tmp.UpdatedAt, tmp.Keyword,
	}}

	return nil
}

func (e* EntryStore) UpdateOrInsertWithKeyword(keyword string, authorID int, description string) error {
	e.Lock()
	defer e.Unlock()

	_, found := e.keywordIndex.Find(keyword)
	if found {
		return e.updateWithKeyword(keyword, authorID, description)
	} else {
		return e.insertWithoutLock(keyword, authorID, description)
	}
}

func (e* EntryStore) DeleteWithKeyword(keyword string) error {
	e.Lock()
	defer e.Unlock()

	id, found := e.keywordIndex.Find(keyword)
	if !found {
		return fmt.Errorf("%s is not used", keyword)
	}
	if e.deleted[id] {
		return fmt.Errorf("%s is already deleted. Index is broken", id)
	}

	e.count--
	e.dbOpChan <- DBOperatorEntry{`DELETE FROM entry WHERE ketword = ?`, []interface{}{keyword}}

	return nil
}

func (e* EntryStore) SelectTopNSkipMOrderByUpdated(limit ,offset int) []Entry {
	e.RLock()
	defer e.RUnlock()

	ids := make([]int, 0, e.count)
	for i, _ := range e.store {
		if !e.deleted[i] {
			ids = append(ids, i)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		return e.store[ids[i]].UpdatedAt.After(e.store[ids[j]].UpdatedAt)
	})

	ret := make([]Entry, 0, limit)
	for i := 0; i < limit; i++ {
		if i + offset > len(ids) {
			break
		}
		id := ids[i + offset]
		ret = append(ret, *e.store[id])
	}

	return ret
}

func (e* EntryStore) GetAllKeywordsOrderByLength() []string {
	e.RLock()

	ret := make([]string, 0, e.count)
	for i, _ := range e.store {
		if !e.deleted[i] {
			ret = append(ret, e.store[i].Keyword)
		}
	}
	e.RUnlock()

	sort.Slice(ret, func(i, j int) bool {
		return len(ret[i]) > len(ret[j])
	})

	return ret
}

func (e* EntryStore) KeywordExists(k string) bool {
	e.RLock()
	defer e.RUnlock()

	_, found := e.keywordIndex.Find(k)
	return found
}

func (e* EntryStore) SelectWithKeyword(k string) (*Entry, error) {
	e.RLock()
	defer e.RUnlock()

	id, found := e.keywordIndex.Find(k)
	if !found {
		return nil, fmt.Errorf("not found")
	}

	ret := *e.store[id]
	return &ret, nil
}

func (e* EntryStore) TotalCount() int {
	e.RLock()
	defer e.RUnlock()

	return e.count
}

func (e* EntryStore) KeywordLastUpdated() int64 {
	e.RLock()
	defer e.RUnlock()

	return e.keywordIndex.lastUpdated
}