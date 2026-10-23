package domain

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/internal/ent"
	"github.com/lesomnus/rove/internal/ent/event"
	"github.com/lesomnus/rove/server/pd"
)

// An event keeps no sentence (payday#151): it keeps the message of its kind
// (`rove/past.proto`), and what it says is made of that when it is read -- in
// the workspace's zone, naming the people in it as they are called then, each
// of them on the access record of the call that reads it (design 8.2). What
// is here is what Rove says of each kind, in Korean, once.
//
// A thing is said as the message holds it -- an asset by the tag it had, a
// reservation by the name it had -- and a person by identifier, named when it
// is read ([who]). The words are the ones each event was written with before
// it kept a message, so that what a page shows reads the same across the two;
// a reservation's time is the one change, said in the workspace's zone where
// it was UTC.

// Sentences is what Rove says of its events.
var Sentences = sentences()

func sentences() *past.Sentences {
	s := past.NewSentences("ko")
	s.TimeLayout("ko", "2006-01-02 15:04")

	// asset.go and register.go
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetAdded) string {
		return fmt.Sprintf("%s 등록", m.GetTag())
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetVoided) string {
		return fmt.Sprintf("%s 등록 취소", m.GetTag())
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetMoved) string {
		if len(m.GetTo()) == 0 {
			return fmt.Sprintf("%s 꺼냄", m.GetTag())
		}
		return fmt.Sprintf("%s → %s", m.GetTag(), m.GetToTag())
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetAttributesSet) string {
		return fmt.Sprintf("%s 속성 변경", m.GetTag())
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetAssigned) string {
		whom := "-"
		if len(m.GetParty()) > 0 {
			whom = who(r, m.GetParty())
		}
		return fmt.Sprintf("%s %s: %s", m.GetTag(), m.GetRole(), whom)
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetRelated) string {
		verb := "연결"
		if m.GetEnd() {
			verb = "연결 끝"
		}
		return fmt.Sprintf("%s %s %s (%s)", m.GetTag(), verb, m.GetTargetTag(), m.GetKind())
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetCorrected) string {
		what := m.GetKey()
		switch idOf(m.GetRow()).Domain() {
		case pd.PlacementDomain:
			what = "배치"
		case pd.LinkDomain:
			what = "관계"
		case pd.StewardshipDomain:
			what = m.GetRole()
		}
		return fmt.Sprintf("%s %s 정정", m.GetTag(), what)
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetsAdded) string {
		return fmt.Sprintf("대량 등록: %d개", m.GetCount())
	})

	// stock.go
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.StockAdded) string {
		return fmt.Sprintf("재고 시작: %s @ %s", m.GetName(), m.GetSpaceName())
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.StockReceived) string {
		return fmt.Sprintf("재고 receive: %s %+d", m.GetName(), m.GetDelta())
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.StockConsumed) string {
		return fmt.Sprintf("재고 consume: %s %+d", m.GetName(), m.GetDelta())
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.StockAdjusted) string {
		return fmt.Sprintf("재고 adjust: %s %+d", m.GetName(), m.GetDelta())
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.StockTransferred) string {
		return fmt.Sprintf("재고 이동: %s %d → %s", m.GetName(), m.GetQuantity(), m.GetToSpaceName())
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.StockConverted) string {
		return fmt.Sprintf("재고 → 자산: %s %d개", m.GetName(), m.GetQuantity())
	})

	// custody.go
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.CustodyIssued) string {
		verb := "지급"
		if m.GetKind() == "loan" {
			verb = "대여"
		}
		return fmt.Sprintf("%s: %s", verb, who(r, m.GetParty()))
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.CustodyAcknowledged) string {
		return "인수 확인"
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.CustodyReturned) string {
		return "반납"
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.CustodyExtended) string {
		return "반납 기한 연장: " + m.GetDue().AsTime().In(r.Zone()).Format(time.DateOnly)
	})

	// count.go
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.CountStarted) string {
		return "실사 시작: " + m.GetName()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.CountScanned) string {
		return "실사 스캔"
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.CountReconciled) string {
		return "실사 대조"
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.CountResolved) string {
		return "실사 결과 처리: " + m.GetResolution()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.CountClosed) string {
		return "실사 종료: " + m.GetName()
	})

	// booking.go and spin.go
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.BookableAdded) string {
		return m.GetTag() + " 예약 가능"
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.BookableUpdated) string {
		return "예약 정책 변경"
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.ReservationAdded) string {
		return fmt.Sprintf("예약: %s %s", m.GetAssetName(), m.GetBeginsAt().AsTime().In(r.Zone()).Format("01-02 15:04"))
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.ReservationApproved) string {
		return "예약 " + resConfirmed + ": " + m.GetName()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.ReservationRejected) string {
		return "예약 " + resRejected + ": " + m.GetName()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.ReservationCancelled) string {
		return "예약 " + resCancelled + ": " + m.GetName()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.ReservationCheckedIn) string {
		return "예약 " + resInUse + ": " + m.GetName()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.ReservationCompleted) string {
		if m.GetEnded() {
			return "예약 종료: " + m.GetName()
		}
		return "예약 " + resCompleted + ": " + m.GetName()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.ReservationConfirmed) string {
		return "예약 확정: " + m.GetName()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.ReservationExpired) string {
		if m.GetFrom() == resRequested {
			return "승인 대기 만료: " + m.GetName()
		}
		return "홀드 만료: " + m.GetName()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.ReservationNoShow) string {
		return "노쇼: " + m.GetName()
	})

	// label.go and spin.go
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.TenantDomainAdded) string {
		return domainWords(m.GetPurpose()) + " 등록: " + m.GetHost()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.TenantDomainVerified) string {
		if m.GetAgain() {
			return domainWords(m.GetPurpose()) + " 다시 확인: " + m.GetHost()
		}
		return domainWords(m.GetPurpose()) + " 확인: " + m.GetHost()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.TenantDomainActivated) string {
		return domainWords(m.GetPurpose()) + " 활성화: " + m.GetHost()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.TenantDomainRetired) string {
		return domainWords(m.GetPurpose()) + " 은퇴: " + m.GetHost()
	})
	// Why it lapsed is the deployment's to say, here, where it once wrote
	// it as the event's reason -- which is a person's words now.
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.TenantDomainLapsed) string {
		return fmt.Sprintf("%s 사용 중지: %s (DNS가 %s부터 이 서비스를 가리키지 않거나 TXT 레코드가 없습니다)",
			domainWords(m.GetPurpose()), m.GetHost(), m.GetFailingSince().AsTime().In(r.Zone()).Format("1월 2일 15:04"))
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.LabelsPrinted) string {
		return "라벨 인쇄: " + m.GetBatch()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.LabelBound) string {
		return m.GetTag() + " 라벨 부착"
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.LabelUnbound) string {
		return "라벨 떼어냄"
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.AttachmentAdded) string {
		return "파일 첨부: " + m.GetName()
	})

	// org.go, depart.go and workspace.go
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.PartyAdded) string {
		return "추가: " + who(r, m.GetParty())
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.PartyUpdated) string {
		return "수정: " + who(r, m.GetParty())
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.PartyInvited) string {
		return fmt.Sprintf("로그인 발급: %s (%s)", who(r, m.GetParty()), alias(r, m.GetHolder()))
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.PartyRoleSet) string {
		switch m.GetBy() {
		case "succession":
			return fmt.Sprintf("소유자 승계: %s → owner (마지막 소유자의 계정 탈퇴)", who(r, m.GetHolder()))
		case "operator":
			return fmt.Sprintf("소유자 지정: %s → owner (운영자)", who(r, m.GetHolder()))
		}
		return fmt.Sprintf("역할 변경: %s → %s", who(r, m.GetParty()), m.GetRole())
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.PartyPasswordSet) string {
		return "비밀번호 변경"
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.PartyDeactivated) string {
		return "로그인 비활성화: " + who(r, m.GetParty())
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.PartyPseudonymized) string {
		return "개인정보 삭제"
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetTypeAdded) string {
		return "유형 추가: " + m.GetName()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.AssetTypeUpdated) string {
		return "유형 수정: " + m.GetName()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.ItemModelAdded) string {
		return "모델 추가: " + strings.TrimSpace(m.GetMaker()+" "+m.GetName())
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.ItemModelUpdated) string {
		return "모델 수정: " + m.GetName()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.AccountDeparted) string {
		return "계정 탈퇴: 개인정보 삭제"
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.WorkspaceConfigured) string {
		return "워크스페이스 설정 변경"
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.WorkspaceClosed) string {
		switch m.GetBy() {
		case ClosedByOperator:
			return "워크스페이스 닫기 (운영자)"
		case ClosedByDeployment:
			return "워크스페이스 닫기 (남은 구성원 없음)"
		}
		return "워크스페이스 닫기"
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.WorkspaceRestored) string {
		return "워크스페이스 복구"
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.WorkspaceImported) string {
		return fmt.Sprintf("워크스페이스 복구 (운영자): %s 내보내기에서", m.GetExportedAt().AsTime().Format(time.DateOnly))
	})

	// invitation.go
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.InvitationAdded) string {
		return "초대 (" + roleWords[m.GetRole()] + ")"
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.InvitationRevoked) string {
		return "초대 취소"
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.InvitationClaimed) string {
		return "초대 링크로 받음"
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.InvitationDenied) string {
		return "초대 거절"
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.InvitationAccepted) string {
		return "초대 수락 (" + roleWords[m.GetRole()] + ")"
	})

	// work.go, access.go and expire.go
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.WorkOrderOpened) string {
		return fmt.Sprintf("작업 열림: %s (%s)", m.GetName(), m.GetTag())
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.WorkOrderUpdated) string {
		return "작업 수정: " + m.GetName()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.WorkOrderDone) string {
		return "작업 완료: " + m.GetName()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.WorkOrderCancelled) string {
		return "작업 취소: " + m.GetName()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.PurchaseAdded) string {
		return "구매 등록: " + m.GetName()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.PurchaseReceived) string {
		return "입고: " + m.GetName()
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.AccessReviewed) string {
		z := r.Zone()
		period := fmt.Sprintf("%s – %s",
			m.GetFrom().AsTime().In(z).Format(time.DateOnly),
			m.GetTo().AsTime().Add(-time.Nanosecond).In(z).Format(time.DateOnly))
		return fmt.Sprintf("접속기록 점검: %s, 호출 %d건 (내려받기 %d건, 거부 %d건)", period, m.GetCalls(), m.GetDownloads(), m.GetRefused())
	})
	past.Say(s, "ko", func(r *past.Reading, ev past.EventRow, m *app.RetentionExpired) string {
		total := m.GetPlacements() + m.GetLinks() + m.GetStewardships() + m.GetFacts() + m.GetEvents() +
			m.GetReservations() + m.GetCustodies() + m.GetCounts() + m.GetWorkOrders() + m.GetStockMovements() +
			m.GetAttachments()
		return fmt.Sprintf("보존 기간이 지난 이력 %d건 삭제 (%s 이전)", total, m.GetBefore().AsTime().In(r.Zone()).Format("2006년 1월 2일"))
	})

	// A row from before Rove's events were the past's: the sentence it was
	// written with, and the people it marks named as they are now.
	s.Legacy(func(r *past.Reading, ev past.EventRow) string {
		n := namerOf(r.Context())
		if n == nil {
			return ""
		}
		v, err := n.legacy(r.Context(), ev.Id)
		if err != nil {
			r.Fail(err)
			return ""
		}
		return n.say(v.desc, v.payload)
	})

	return s
}

// who is the person or membership `b` names, as a sentence says one: as they
// are called when it is read -- a membership by the person record it is, as
// whoever did something is named -- each on the access record of the call
// that reads it; and "?" where nothing names it, as Rove has always said one.
func who(r *past.Reading, b []byte) string {
	id := idOf(b)
	if id.IsZero() {
		return "?"
	}
	if v := r.Name(id); v != id.String() {
		return v
	}
	return "?"
}

// alias is a membership by the name it signs in with, `@` and it, or the
// pseudonym of somebody forgotten: what the namer the reading is for says,
// which puts whose it is on the access record as [who] does.
func alias(r *past.Reading, b []byte) string {
	n := namerOf(r.Context())
	id := idOf(b)
	if n == nil || id.IsZero() {
		return "?"
	}
	return n.member(id.Uuid())
}

// namerKey is the namer of the answer being said, on the context its reading
// is for: what a sentence that says more than [past.Reading.Name] does --
// a membership's alias, a row from before -- reads with.
type namerKey struct{}

func withNamer(ctx context.Context, n *namer) context.Context {
	return context.WithValue(ctx, namerKey{}, n)
}

func namerOf(ctx context.Context) *namer {
	n, _ := ctx.Value(namerKey{}).(*namer)
	return n
}

// reading is how an answer of this layer says its events: in Korean, in the
// workspace's zone, its people named by `n` as they are called now.
func (t *Tx) reading(n *namer) *past.Reading {
	return Sentences.Read(withNamer(t.ctx, n), n, "ko", t.zone())
}

// said is a row of Rove's Event as payday's past reads one.
func said(v *ent.Event) past.EventRow {
	r := past.EventRow{
		Id:       pdid.Id(v.Id),
		Tenant:   pdid.Id(v.TenantId),
		Kind:     v.Kind,
		At:       v.OccurredAt,
		Reason:   v.Reason,
		Trace:    v.TraceId,
		Value:    v.Value,
		Recorded: v.DateCreated,
	}
	if v.ActorId != nil {
		r.Actor = pdid.Id(*v.ActorId)
	}
	if v.SubjectId != nil {
		r.Subject = pdid.Id(*v.SubjectId)
	}
	if v.CauseId != nil {
		r.Cause = pdid.Id(*v.CauseId)
	}
	return r
}

// legacyRow is what a row from before kept of what it said: the sentence,
// and what it named.
type legacyRow struct{ desc, payload string }

// legacy is what the row `id` said, read once: off what the answer read of
// it already, or else off the row.
func (n *namer) legacy(ctx context.Context, id pdid.Id) (legacyRow, error) {
	if v, ok := n.legacies[id.Uuid()]; ok {
		return v, nil
	}
	v, err := n.t.db.Event.Query().
		Where(event.Id(id.Uuid()), event.TenantId(n.t.tenant.Uuid())).
		Select(event.FieldDesc, event.FieldPayload).
		Only(ctx)
	if err != nil {
		return legacyRow{}, err
	}
	n.knows(v.Id, v.Desc, v.Payload)
	return n.legacies[v.Id], nil
}

// before reads what the rows from before among `rows` said, the ones it does
// not know yet, in one read: a row that holds no message is one of them.
func (n *namer) before(rows ...map[uuid.UUID]past.EventRow) error {
	var ids []uuid.UUID
	for _, m := range rows {
		for k, v := range m {
			if _, ok := n.legacies[k]; !ok && v.Value == nil {
				ids = append(ids, k)
			}
		}
	}
	for page := range slices.Chunk(ids, 500) {
		vs, err := n.t.db.Event.Query().
			Where(event.IdIn(page...), event.TenantId(n.t.tenant.Uuid())).
			Select(event.FieldId, event.FieldDesc, event.FieldPayload).
			All(n.t.ctx)
		if err != nil {
			return err
		}
		for _, v := range vs {
			n.knows(v.Id, v.Desc, v.Payload)
		}
	}
	return nil
}

// knows is what a row from before said, read with it.
func (n *namer) knows(id uuid.UUID, desc, payload string) {
	if n.legacies == nil {
		n.legacies = map[uuid.UUID]legacyRow{}
	}
	n.legacies[id] = legacyRow{desc, payload}
}
