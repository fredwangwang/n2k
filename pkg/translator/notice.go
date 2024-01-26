package translator

import (
	"fmt"
	"strings"

	"golang.org/x/exp/slices"
)

type NoticeImportance int

const (
	NoticeBroken NoticeImportance = iota
	NoticeImportant
	NoticeInformative
)

func (ni NoticeImportance) String() string {
	switch ni {
	case NoticeBroken:
		return "Broken"
	case NoticeImportant:
		return "Important"
	case NoticeInformative:
		return "Informative"
	default:
		panic("wtf?")
	}
}

type NoticeItem struct {
	Importance NoticeImportance
	Msg        string
}

type Notices []NoticeItem

func (ns Notices) String() string {
	ns = ns.UniqueNotices()

	builder := strings.Builder{}
	currentImportance := -1
	for _, n := range ns {
		if currentImportance != int(n.Importance) {
			builder.WriteString("\n# " + n.Importance.String() + "\n\n")
			currentImportance = int(n.Importance)
		}
		builder.WriteString(fmt.Sprintf("* %s\n", n.Msg))
	}

	return builder.String()
}

func (ns Notices) Sort() {
	slices.SortStableFunc[NoticeItem](ns, func(a, b NoticeItem) bool {
		if a.Importance != b.Importance {
			return a.Importance < b.Importance
		}
		return a.Msg < b.Msg
	})
}

func (ns Notices) UniqueNotices() Notices {
	if len(ns) == 0 {
		return ns
	}

	ns.Sort()

	res := Notices{ns[0]}

	for _, n := range ns[1:] {
		if res[len(res)-1] == n {
			continue
		}
		res = append(res, n)
	}
	return res
}
