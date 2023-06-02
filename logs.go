package main

import (
	"sync"
	"time"

	"golang.org/x/exp/slog"
)

var (
	maxBufferedLogs = 100
)

type idRecord struct {
	id     string
	record slog.Record
}

type _ interface {
	WriteLog(id string, r slog.Record)
	Listen(id string) <-chan struct{}
	Query(id string, after time.Time, limit int) []slog.Record
}

type LogStorage struct {
	subLock     sync.Mutex
	subscribers map[string][]<-chan struct{}

	writeLock     sync.RWMutex
	pendingWrites []idRecord
}

func (ls *LogStorage) WriteLog(id string, record slog.Record) {
	ls.writeLock.Lock()
	ls.pendingWrites = append(ls.pendingWrites, idRecord{id: id, record: record})
	if len(ls.pendingWrites) > maxBufferedLogs+20 {
		ls.pendingWrites = ls.pendingWrites[20:]
	}
	ls.writeLock.Unlock()
	ls.subLock.Lock()
	subs := ls.subscribers[id]
	delete(ls.subscribers, id)
	ls.subLock.Unlock()
	_ = subs
}

func (ls *LogStorage) Listen(id string) <-chan struct{} {
	return nil
}
func (ls *LogStorage) Query(id string, after time.Time, limit int) []slog.Record {
	return nil
}
