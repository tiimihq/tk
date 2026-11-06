package board

// Store is everything the board does with issues; tk implements it per backend.
type Store interface {
	Load() (items []Issue, sessionKeys []string, err error) // sessionKeys: issues an agent is working on
	Create(title string) (key string, err error)
	Update(key, field, value string) error // field: title, description, priority, status
	Comment(key, body string) error
	Cancel(key string) error
}

// Issue, Comment and Cost are what the board shows.
type (
	Issue   = issueDetail
	Comment = commentView
	Cost    = costTotals
)

type issueDetail struct {
	Identifier  string        `json:"identifier"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Status      string        `json:"status"`
	Priority    string        `json:"priority"`
	Assignee    string        `json:"assignee"`
	CreatedAt   string        `json:"createdAt"`
	UpdatedAt   string        `json:"updatedAt"`
	Comments    []commentView `json:"comments"`
	Cost        costTotals    `json:"cost"`
	CostText    string        `json:"costText"`
}

type commentView struct {
	Author    string `json:"author"`
	Kind      string `json:"kind"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
}

type costTotals struct {
	Events   int
	Cents    float64
	Unpriced int
}

var localPriorities = []string{"urgent", "high", "medium", "low", "none"}

func oneOf(v string, allowed []string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

func isClosed(status string) bool { return status == "done" || status == "cancelled" }
