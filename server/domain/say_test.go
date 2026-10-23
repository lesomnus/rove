package domain

import (
	"context"
	"maps"
	"slices"
	"testing"
	"time"
	"uuid"

	"github.com/lesomnus/payday/past"
	"github.com/lesomnus/payday/pdid"
	"github.com/lesomnus/z"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	app "github.com/lesomnus/rove"
	"github.com/lesomnus/rove/server/pd"
)

// TestEveryKindIsSaidInKorean: no kind of Rove's events is read as its fields
// (payday#151) -- a kind declared and said nowhere would be.
func TestEveryKindIsSaidInKorean(t *testing.T) {
	require.Empty(t, past.Unsaid(Sentences, "ko", pd.EventKinds))
}

// TestEveryKindIsSaidAsItWasWritten: what each kind says, made of its message
// as it is read, is the sentence an event of it was written with before it
// kept a message -- `desc`, word for word -- so that a page reads the same
// across the two. A person is named as they are called when it is read, and
// "?" where nothing names them. The one change: a reservation's time is in
// the workspace's zone, where it was UTC.
func TestEveryKindIsSaidAsItWasWritten(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()

	tenant := pdid.New(pd.TenantDomain)
	kim, kimHolder := pdid.New(pd.PartyDomain), pdid.New(pd.HolderDomain)
	nobody := pdid.New(pd.PartyDomain)
	asset, other := pdid.New(pd.AssetDomain), pdid.New(pd.AssetDomain)
	stock := pdid.New(pd.StockDomain)
	seoul, err := time.LoadLocation("Asia/Seoul")
	x.NoError(err)
	// 10:30 in Seoul.
	at := time.Date(2026, 10, 24, 1, 30, 0, 0, time.UTC)

	n := &namer{
		t:       &Tx{ctx: ctx, tenant: tenant},
		parties: map[uuid.UUID]called{kim.Uuid(): {found: true, name: "김철수"}},
		holders: map[uuid.UUID]called{kimHolder.Uuid(): {found: true, name: "김철수"}},
		members: map[uuid.UUID]called{kimHolder.Uuid(): {found: true, name: "@kim"}},
	}
	for _, id := range []pdid.Id{nobody} {
		n.parties[id.Uuid()] = called{}
	}
	r := Sentences.Read(withNamer(ctx, n), n, "ko", seoul)

	row := func(v proto.Message) past.EventRow {
		value, err := anypb.New(v)
		x.NoError(err)
		return past.EventRow{Id: pdid.New(pd.EventDomain), Tenant: tenant, Kind: kindFor(v), Value: value}
	}

	said := map[string]bool{}
	for _, c := range []struct {
		v    proto.Message
		want string
	}{
		{app.AssetAdded_builder{Asset: asset.Bytes(), Tag: "NB-1", Name: "노트북", Kind: "item"}.Build(), "NB-1 등록"},
		{app.AssetVoided_builder{Asset: asset.Bytes(), Tag: "NB-1"}.Build(), "NB-1 등록 취소"},
		{app.AssetMoved_builder{Asset: asset.Bytes(), Tag: "NB-1", To: other.Bytes(), ToTag: "S-00001", Mode: "located"}.Build(), "NB-1 → S-00001"},
		{app.AssetMoved_builder{Asset: asset.Bytes(), Tag: "NB-1"}.Build(), "NB-1 꺼냄"},
		{app.AssetAttributesSet_builder{Asset: asset.Bytes(), Tag: "NB-1", Set: []string{"status"}, Cleared: []string{"attr.cpu"}}.Build(), "NB-1 속성 변경"},
		{app.AssetAssigned_builder{Asset: asset.Bytes(), Tag: "NB-1", Role: "owner", Party: kim.Bytes()}.Build(), "NB-1 owner: 김철수"},
		{app.AssetAssigned_builder{Asset: asset.Bytes(), Tag: "NB-1", Role: "manager"}.Build(), "NB-1 manager: -"},
		{app.AssetAssigned_builder{Asset: asset.Bytes(), Tag: "NB-1", Role: "owner", Party: nobody.Bytes()}.Build(), "NB-1 owner: ?"},
		{app.AssetRelated_builder{Asset: asset.Bytes(), Tag: "NB-1", Target: other.Bytes(), TargetTag: "K-00001", Kind: "member_of", Required: true}.Build(), "NB-1 연결 K-00001 (member_of)"},
		{app.AssetRelated_builder{Asset: asset.Bytes(), Tag: "NB-1", Target: other.Bytes(), TargetTag: "K-00001", Kind: "member_of", End: true}.Build(), "NB-1 연결 끝 K-00001 (member_of)"},
		{app.AssetCorrected_builder{Asset: asset.Bytes(), Tag: "NB-1", Row: pdid.New(pd.PlacementDomain).Bytes(), Retract: true}.Build(), "NB-1 배치 정정"},
		{app.AssetCorrected_builder{Asset: asset.Bytes(), Tag: "NB-1", Row: pdid.New(pd.LinkDomain).Bytes(), ValidFrom: timestamppb.New(at)}.Build(), "NB-1 관계 정정"},
		{app.AssetCorrected_builder{Asset: asset.Bytes(), Tag: "NB-1", Row: pdid.New(pd.StewardshipDomain).Bytes(), Role: "owner"}.Build(), "NB-1 owner 정정"},
		{app.AssetCorrected_builder{Asset: asset.Bytes(), Tag: "NB-1", Row: pdid.New(pd.FactDomain).Bytes(), Key: "attr.cpu"}.Build(), "NB-1 attr.cpu 정정"},
		{app.AssetsAdded_builder{Count: 3}.Build(), "대량 등록: 3개"},

		{app.StockAdded_builder{Stock: stock.Bytes(), Name: "A4 용지", SpaceName: "2층 창고", Quantity: 5}.Build(), "재고 시작: A4 용지 @ 2층 창고"},
		{app.StockReceived_builder{Stock: stock.Bytes(), Name: "A4 용지", Delta: 5}.Build(), "재고 receive: A4 용지 +5"},
		{app.StockConsumed_builder{Stock: stock.Bytes(), Name: "A4 용지", Delta: -2}.Build(), "재고 consume: A4 용지 -2"},
		{app.StockAdjusted_builder{Stock: stock.Bytes(), Name: "A4 용지", Delta: -1}.Build(), "재고 adjust: A4 용지 -1"},
		{app.StockTransferred_builder{Stock: stock.Bytes(), Name: "A4 용지", Quantity: 3, ToSpaceName: "본관"}.Build(), "재고 이동: A4 용지 3 → 본관"},
		{app.StockConverted_builder{Stock: stock.Bytes(), Name: "모니터", Quantity: 2}.Build(), "재고 → 자산: 모니터 2개"},

		{app.CustodyIssued_builder{Party: kim.Bytes(), Kind: "issue", Lines: 2}.Build(), "지급: 김철수"},
		{app.CustodyIssued_builder{Party: kim.Bytes(), Kind: "loan", Due: timestamppb.New(at), Asset: asset.Bytes()}.Build(), "대여: 김철수"},
		{app.CustodyAcknowledged_builder{}.Build(), "인수 확인"},
		{app.CustodyReturned_builder{Asset: asset.Bytes()}.Build(), "반납"},
		// 05:00 in Seoul, on the 25th.
		{app.CustodyExtended_builder{Due: timestamppb.New(time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC))}.Build(), "반납 기한 연장: 2026-10-25"},

		{app.CountStarted_builder{Name: "본관 실사 2026-10-24"}.Build(), "실사 시작: 본관 실사 2026-10-24"},
		{app.CountScanned_builder{Asset: asset.Bytes(), Code: "X"}.Build(), "실사 스캔"},
		{app.CountReconciled_builder{Missing: 4}.Build(), "실사 대조"},
		{app.CountResolved_builder{Resolution: "moved"}.Build(), "실사 결과 처리: moved"},
		{app.CountClosed_builder{Name: "본관 실사"}.Build(), "실사 종료: 본관 실사"},

		{app.BookableAdded_builder{Asset: asset.Bytes(), Tag: "S-00002"}.Build(), "S-00002 예약 가능"},
		{app.BookableUpdated_builder{Asset: asset.Bytes()}.Build(), "예약 정책 변경"},
		// The one sentence said otherwise than it was written: in the
		// workspace's zone, where it was "10-24 01:30".
		{app.ReservationAdded_builder{Party: kim.Bytes(), Status: "confirmed", BeginsAt: timestamppb.New(at), AssetName: "회의실"}.Build(), "예약: 회의실 10-24 10:30"},
		{app.ReservationApproved_builder{Name: "주간 회의"}.Build(), "예약 confirmed: 주간 회의"},
		{app.ReservationRejected_builder{Name: "주간 회의"}.Build(), "예약 rejected: 주간 회의"},
		{app.ReservationCancelled_builder{Name: "주간 회의", Series: true}.Build(), "예약 cancelled: 주간 회의"},
		{app.ReservationCheckedIn_builder{Name: "주간 회의"}.Build(), "예약 in_use: 주간 회의"},
		{app.ReservationCompleted_builder{Name: "주간 회의"}.Build(), "예약 completed: 주간 회의"},
		{app.ReservationCompleted_builder{Name: "주간 회의", Ended: true}.Build(), "예약 종료: 주간 회의"},
		{app.ReservationConfirmed_builder{Name: "주간 회의", To: "requested"}.Build(), "예약 확정: 주간 회의"},
		{app.ReservationExpired_builder{Name: "주간 회의", From: "held"}.Build(), "홀드 만료: 주간 회의"},
		{app.ReservationExpired_builder{Name: "주간 회의", From: "requested"}.Build(), "승인 대기 만료: 주간 회의"},
		{app.ReservationNoShow_builder{Name: "주간 회의"}.Build(), "노쇼: 주간 회의"},

		{app.TenantDomainAdded_builder{Host: "assets.acme.example", Purpose: "app", Source: "custom"}.Build(), "워크스페이스 도메인 등록: assets.acme.example"},
		{app.TenantDomainAdded_builder{Host: "acme.l.test", Purpose: "label", Source: "default"}.Build(), "라벨 도메인 등록: acme.l.test"},
		{app.TenantDomainVerified_builder{Host: "assets.acme.example", Purpose: "app"}.Build(), "워크스페이스 도메인 확인: assets.acme.example"},
		{app.TenantDomainVerified_builder{Host: "assets.acme.example", Purpose: "app", Again: true}.Build(), "워크스페이스 도메인 다시 확인: assets.acme.example"},
		{app.TenantDomainActivated_builder{Host: "assets.acme.example", Purpose: "app"}.Build(), "워크스페이스 도메인 활성화: assets.acme.example"},
		{app.TenantDomainRetired_builder{Host: "acme.l.test", Purpose: "label", Force: true, Labels: 3}.Build(), "라벨 도메인 은퇴: acme.l.test"},
		// What was the reason, rove's own words, said by the sentence.
		{app.TenantDomainLapsed_builder{Host: "assets.acme.example", Purpose: "app", FailingSince: timestamppb.New(time.Date(2026, 10, 23, 3, 0, 0, 0, time.UTC)), Failures: 6}.Build(),
			"워크스페이스 도메인 사용 중지: assets.acme.example (DNS가 10월 23일 12:00부터 이 서비스를 가리키지 않거나 TXT 레코드가 없습니다)"},
		{app.LabelsPrinted_builder{Batch: "2026-10-24 10:30", Count: 20}.Build(), "라벨 인쇄: 2026-10-24 10:30"},
		{app.LabelBound_builder{Asset: asset.Bytes(), Tag: "NB-1"}.Build(), "NB-1 라벨 부착"},
		{app.LabelUnbound_builder{Asset: asset.Bytes(), Void: true}.Build(), "라벨 떼어냄"},
		{app.AttachmentAdded_builder{Subject: asset.Bytes(), Name: "영수증.pdf", ContentType: "application/pdf", Size: 1024}.Build(), "파일 첨부: 영수증.pdf"},

		{app.PartyAdded_builder{Party: kim.Bytes(), Kind: "person"}.Build(), "추가: 김철수"},
		{app.PartyUpdated_builder{Party: kim.Bytes()}.Build(), "수정: 김철수"},
		{app.PartyUpdated_builder{Party: nobody.Bytes()}.Build(), "수정: ?"},
		{app.PartyInvited_builder{Party: kim.Bytes(), Holder: kimHolder.Bytes(), Role: "member"}.Build(), "로그인 발급: 김철수 (@kim)"},
		{app.PartyRoleSet_builder{Holder: kimHolder.Bytes(), Party: kim.Bytes(), Role: "manager"}.Build(), "역할 변경: 김철수 → manager"},
		{app.PartyRoleSet_builder{Holder: kimHolder.Bytes(), Role: "owner", By: "succession"}.Build(), "소유자 승계: 김철수 → owner (마지막 소유자의 계정 탈퇴)"},
		{app.PartyRoleSet_builder{Holder: kimHolder.Bytes(), Role: "owner", By: "operator"}.Build(), "소유자 지정: 김철수 → owner (운영자)"},
		{app.PartyPasswordSet_builder{Holder: kimHolder.Bytes(), Own: true}.Build(), "비밀번호 변경"},
		{app.PartyDeactivated_builder{Party: kim.Bytes(), Holder: kimHolder.Bytes()}.Build(), "로그인 비활성화: 김철수"},
		{app.PartyPseudonymized_builder{Held: 2}.Build(), "개인정보 삭제"},
		{app.AssetTypeAdded_builder{Name: "노트북", Kind: "item"}.Build(), "유형 추가: 노트북"},
		{app.AssetTypeUpdated_builder{Name: "노트북"}.Build(), "유형 수정: 노트북"},
		{app.ItemModelAdded_builder{Name: "ThinkPad X1", Maker: "Lenovo"}.Build(), "모델 추가: Lenovo ThinkPad X1"},
		{app.ItemModelAdded_builder{Name: "X1"}.Build(), "모델 추가: X1"},
		{app.ItemModelUpdated_builder{Name: "ThinkPad X1"}.Build(), "모델 수정: ThinkPad X1"},
		{app.AccountDeparted_builder{Ownerless: true}.Build(), "계정 탈퇴: 개인정보 삭제"},
		{app.WorkspaceConfigured_builder{KeepDays: z.Ptr(uint32(365))}.Build(), "워크스페이스 설정 변경"},
		{app.WorkspaceClosed_builder{By: "owner", Destroy: timestamppb.New(at)}.Build(), "워크스페이스 닫기"},
		{app.WorkspaceClosed_builder{By: "operator"}.Build(), "워크스페이스 닫기 (운영자)"},
		{app.WorkspaceClosed_builder{By: "deployment"}.Build(), "워크스페이스 닫기 (남은 구성원 없음)"},
		{app.WorkspaceRestored_builder{By: "owner"}.Build(), "워크스페이스 복구"},
		{app.WorkspaceImported_builder{ExportedAt: timestamppb.New(time.Date(2026, 10, 20, 23, 0, 0, 0, time.UTC)), Rows: 120}.Build(), "워크스페이스 복구 (운영자): 2026-10-20 내보내기에서"},

		{app.InvitationAdded_builder{Role: "manager"}.Build(), "초대 (매니저)"},
		{app.InvitationRevoked_builder{}.Build(), "초대 취소"},
		{app.InvitationClaimed_builder{}.Build(), "초대 링크로 받음"},
		{app.InvitationDenied_builder{}.Build(), "초대 거절"},
		{app.InvitationAccepted_builder{Role: "member", Holder: kimHolder.Bytes()}.Build(), "초대 수락 (구성원)"},

		{app.WorkOrderOpened_builder{Asset: asset.Bytes(), Tag: "NB-1", Name: "화면 교체", Kind: "repair", Status: "open"}.Build(), "작업 열림: 화면 교체 (NB-1)"},
		{app.WorkOrderUpdated_builder{Name: "화면 교체", Status: "in_progress"}.Build(), "작업 수정: 화면 교체"},
		{app.WorkOrderDone_builder{Name: "화면 교체", Cost: 30000, Currency: "KRW"}.Build(), "작업 완료: 화면 교체"},
		{app.WorkOrderCancelled_builder{Name: "화면 교체"}.Build(), "작업 취소: 화면 교체"},
		{app.PurchaseAdded_builder{Name: "구매 2026-10-24", Total: 100, Currency: "KRW", Lines: 1}.Build(), "구매 등록: 구매 2026-10-24"},
		{app.PurchaseReceived_builder{Name: "노트북 2대"}.Build(), "입고: 노트북 2대"},
		// The first of October in Seoul to the last.
		{app.AccessReviewed_builder{
			From: timestamppb.New(time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC)), To: timestamppb.New(time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC)),
			Calls: 12, Downloads: 1, Refused: 2,
		}.Build(), "접속기록 점검: 2026-10-01 – 2026-10-31, 호출 12건 (내려받기 1건, 거부 2건)"},
		{app.RetentionExpired_builder{Before: timestamppb.New(time.Date(2026, 4, 27, 15, 0, 0, 0, time.UTC)), Placements: 2, Facts: 3, Events: 1, Reservations: 1}.Build(),
			"보존 기간이 지난 이력 7건 삭제 (2026년 4월 28일 이전)"},
	} {
		v := row(c.v)
		x.Equal(c.want, r.Say(v), v.Kind)
		said[v.Kind] = true
	}
	x.NoError(r.Err())
	x.Empty(slices.DeleteFunc(slices.Sorted(maps.Keys(pd.EventKinds)), func(k string) bool { return said[k] }), "a kind with no sentence here")
}

// TestAnEventFromBeforeIsSaidAsItWasWritten: a row from before Rove's events
// kept a message holds none, and is said by the words it was written with,
// the people it marks named as they are now.
func TestAnEventFromBeforeIsSaidAsItWasWritten(t *testing.T) {
	x := require.New(t)
	ctx := context.Background()
	tenant := pdid.New(pd.TenantDomain)
	kim := pdid.New(pd.PartyDomain)
	n := &namer{t: &Tx{ctx: ctx, tenant: tenant}, parties: map[uuid.UUID]called{kim.Uuid(): {found: true, name: "김 대리"}}}
	r := Sentences.Read(withNamer(ctx, n), n, "ko", time.UTC)

	for _, c := range []struct{ desc, payload, want string }{
		{"수정: {party}", "party=" + kim.String(), "수정: 김 대리"},
		{"역할 변경: Kim → admin", "", "역할 변경: Kim → admin"},
		{"수정: {party}", "party=Kim", "수정: {party}"},
	} {
		id := pdid.New(pd.EventDomain)
		n.knows(id.Uuid(), c.desc, c.payload)
		x.Equal(c.want, r.Say(past.EventRow{Id: id, Tenant: tenant, Kind: "party.update"}))
	}
	x.NoError(r.Err())
}

// kindFor is the kind an event holding `v` is written under, as the schema
// declares it.
func kindFor(v proto.Message) string {
	for k, typ := range pd.EventKinds {
		if typ.Descriptor().FullName() == v.ProtoReflect().Descriptor().FullName() {
			return k
		}
	}
	return ""
}
