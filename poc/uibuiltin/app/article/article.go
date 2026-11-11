package article

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ryan053857/mygoravel/errorx"
)

// Article 是演示用的文章，存在内存里。
type Article struct {
	ID        int
	Title     string
	Slug      string
	Status    string // draft | published | archived
	Views     int
	CreatedAt time.Time
}

// Changes 是新建、修改文章的表单；验证规则与 JSON API 共用 Huma 标签。
type Changes struct {
	Title  string `json:"title" minLength:"1" maxLength:"60"`
	Slug   string `json:"slug" maxLength:"60" pattern:"^[a-z0-9]+(-[a-z0-9]+)*$" patternDescription:"小写字母、数字和连字符"`
	Status string `json:"status" enum:"draft,published,archived"`
}

// ErrSlugTaken 是业务规则错误：slug 已被占用。
var ErrSlugTaken = errorx.Define(errorx.Conflict, "article.slug_taken")

// ErrNotFound 表示文章不存在。
var ErrNotFound = errorx.Define(errorx.NotFound, "article.not_found")

// Query 是列表的筛选、排序与分页参数。
type Query struct {
	Q       string
	Status  string
	Sort    string // title | -title | views | -views | -created（默认）
	Page    int
	PerPage int
}

// Store 是并发安全的内存存储。
type Store struct {
	mu   sync.Mutex
	rows []Article
	next int
}

func NewStore() *Store {
	s := &Store{next: 1}
	words := []string{"Go", "htmx", "templ", "Tailwind", "SSE", "队列", "会话", "缓存", "租户", "Webhook", "权限", "通知"}
	statuses := []string{"draft", "published", "archived"}
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for i := range 57 {
		w := words[i%len(words)]
		s.rows = append(s.rows, Article{
			ID: s.next, Title: fmt.Sprintf("%s 实践 #%d", w, i+1), Slug: fmt.Sprintf("post-%d", i+1),
			Status: statuses[i%3], Views: (i * 37) % 1000, CreatedAt: base.Add(time.Duration(i) * 7 * time.Hour),
		})
		s.next++
	}
	return s
}

// List 返回当前页和筛选后的总数。
func (s *Store) List(q Query) ([]Article, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Article
	for _, a := range s.rows {
		if q.Status != "" && a.Status != q.Status {
			continue
		}
		if q.Q != "" && !strings.Contains(strings.ToLower(a.Title+" "+a.Slug), strings.ToLower(q.Q)) {
			continue
		}
		out = append(out, a)
	}
	slices.SortStableFunc(out, func(a, b Article) int {
		switch q.Sort {
		case "title":
			return strings.Compare(a.Title, b.Title)
		case "-title":
			return strings.Compare(b.Title, a.Title)
		case "views":
			return a.Views - b.Views
		case "-views":
			return b.Views - a.Views
		default:
			return b.CreatedAt.Compare(a.CreatedAt)
		}
	})
	total := len(out)
	start := min((q.Page-1)*q.PerPage, total)
	end := min(start+q.PerPage, total)
	return out[start:end], total
}

func (s *Store) Get(id int) (Article, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.rows {
		if a.ID == id {
			return a, true
		}
	}
	return Article{}, false
}

func (s *Store) slugTaken(slug string, except int) bool {
	for _, a := range s.rows {
		if a.Slug == slug && a.ID != except {
			return true
		}
	}
	return false
}

func (s *Store) Create(c Changes) (Article, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slugTaken(c.Slug, 0) {
		return Article{}, ErrSlugTaken.New()
	}
	a := Article{ID: s.next, Title: c.Title, Slug: c.Slug, Status: c.Status, CreatedAt: time.Now()}
	s.next++
	s.rows = append(s.rows, a)
	return a, nil
}

func (s *Store) Update(id int, c Changes) (Article, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.slugTaken(c.Slug, id) {
		return Article{}, ErrSlugTaken.New()
	}
	for i := range s.rows {
		if s.rows[i].ID == id {
			s.rows[i].Title, s.rows[i].Slug, s.rows[i].Status = c.Title, c.Slug, c.Status
			return s.rows[i], nil
		}
	}
	return Article{}, ErrNotFound.New()
}

func (s *Store) Delete(id int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rows {
		if s.rows[i].ID == id {
			s.rows = slices.Delete(s.rows, i, i+1)
			return true
		}
	}
	return false
}
