# Rove 개발 계획

> 상태: v1.1 (2026-10-10) — self-host, 테넌트 라벨 도메인, payday 이슈 반영  
> 기준 설계: [design.md](design.md). 이 문서는 무엇을 어떤 순서로 만들고, 무엇으로 끝났다고 판단하는지를 정한다.  
> 설계 결정을 바꿀 때는 design.md 12장(결정 기록)을 먼저 고치고, 이 문서는 그에 맞춘다.

## 1. 선행 확인 결과 (Phase 0 스파이크, 2026-10-10)

코드를 쓰기 전에 설계가 기대는 전제를 실제로 돌려 확인했다.

- **확인 환경**: payday의 복사본(`internal/apptest`), PostgreSQL 18.6 컨테이너, pg_bigm 1.2(소스 빌드), 각 관리형 DB의 공식 문서. payday는 처음에 로컬 체크아웃 `5fb4c99`로 돌렸고, 최신 main `daa35c0`에서 다시 돌려 같은 결과를 얻었다.
- 확인용 코드는 payday 내부 패키지에 기대므로 이 저장소에 넣지 않았다. 여기에는 결과만 남긴다.

| # | 확인한 것 | 방법 | 결과 | 반영 |
| --- | --- | --- | --- | --- |
| V1 | ent 스키마 밖의 PG 객체 | apptest ent 스키마로 Plan → Apply → `entschema.Check` → Plan | 같은 디렉터리에 두면 다음 Plan이 `EXCLUDE`·`UNIQUE`·부분 인덱스·FK를 `DROP`하려 한다. 복합 FK가 있으면 Check가 "스키마 불일치"로 서버 시작을 거부한다. 다른 디렉터리에 두면 Plan은 아무것도 하지 않는다 | design 9.4, D12 |
| V2 | 트리거로 같은 테넌트 확인 | 별도 디렉터리의 트리거, Check, 다른 테넌트를 가리키는 INSERT | Check 통과, 그 뒤의 ent 마이그레이션 적용 정상, 다른 테넌트 참조는 SQLSTATE 23503으로 거부 | design 6장·9.4, D11 |
| V3 | 두 디렉터리의 버전 순서 | 마지막 적용 버전보다 오래된 파일 추가 | 어느 디렉터리든 "out of order"로 거부(한 디렉터리 안에서도 같다) | design 9.4 |
| V4 | `seal` 레이어 | 생성 Add를 직접 호출, batch로 호출 | 둘 다 PermissionDenied. batch 오류는 `ops[0] /app.CellService/Add`처럼 작업을 짚는다 | design 9.3 |
| V5 | 도메인 트랜잭션 | `dialect.BeginTx` + `enttx.Rebind`로 두 행 쓰기 | 함께 커밋되고, 실패하면 두 행과 trail이 모두 취소된다 | design 9.3 |
| V6 | batch 안의 도메인 트랜잭션 | batch [도메인 작업, 유일 제약에 걸리는 작업] | 바깥 트랜잭션에 합류한다. 뒤 작업이 실패하면 앞의 도메인 작업까지 취소되고, 성공한 batch는 모든 쓰기를 커밋한다 | design 9.3 |
| V7 | Gate의 엣지 확인 | payday 테스트 `TestAnEdgeIsARead`, `TestAnEdgeThatCanMoveIsAskedAgainWhenItDoes`, Gate 위에 둔 도메인 레이어 | Add·Patch가 다른 테넌트 행을 가리키면 NotFound. Gate 위의 도메인 작업이 내부에서 시도해도 NotFound | design 6장, D20 |
| V8 | `agrees` | payday 테스트 `TestPathsThatMustAgree`, 생성기 코드(`emitTenancy`) | 운영자 스택에서도, 두 테넌트를 다 보는 호출자에게도 InvalidArgument. 엣지가 비어 있으면 비교하지 않는다 | design 6장 |
| V9 | Holder overlay로 인덱스 추가 | protobuf-merge로 `(orm.message)` 인덱스가 든 overlay 병합 | 필드는 합쳐지고 인덱스 옵션은 버려진다 | `Identity` 엔터티 |
| V10 | 배치 트리의 동시 순환 | psql 두 세션에서 a→b, c→d 동시 이동 | 행 잠금만 하면 둘 다 성공해 순환이 생긴다. 테넌트 잠금 행을 먼저 잡으면 두 번째가 거부된다 | design 3.2, D9 |
| V11 | `EXCLUDE`와 부분 유일 인덱스 | psql | 대체 후 삽입은 성공하고 순서를 바꾸면 거부된다. 차단·독점 할당만 겹침이 금지되고, 반열린 구간이 맞닿는 것은 허용된다. Fact는 같은 시각의 현재 값 중복이 거부된다 | design 3.3·4장 |
| V12 | 한국어 검색 | PG 18 + pg_bigm GIN, 20만 행 | `%의자%`는 인덱스 사용, `%의%`(1음절)는 순차 탐색 | design 9.5, D22 |
| V13 | 관리형 DB 확장 지원 | AWS·GCP·Azure·네이버·NHN 공식 문서 | btree_gist·pg_bigm을 모두 지원하는 곳은 AWS RDS와 Cloud SQL이다 | design 9.5(관리형으로 옮길 때 참고) |

1차 검토 때 확인한 것: PostgreSQL FK 검사는 RLS를 우회한다. `pg_trgm`은 2음절 질의에 인덱스를 쓰지 못하고, libc `C` 로케일에서는 한글을 무시한다.

## 2. 진행 원칙

- 추정치는 1인 전업 기준의 대략적인 값이고 약속이 아니다. 마일스톤이 끝날 때마다 다시 잡는다.
- 마일스톤의 완료 기준은 테스트로 고정한다. 테스트로 표현할 수 없는 기준은 시연 절차로 적는다.
- 도메인 규칙(시간 행, 충돌, 테넌트)에 관한 테스트는 PostgreSQL에서 돌린다. SQLite 통과만으로는 끝난 것이 아니다.
- API를 먼저 굳히고 UI를 얹는다. 시간 모델이 흔들리는 동안에는 화면을 만들지 않는다.

## 3. 저장소 구성

`pd new github.com/lesomnus/rove .`가 만드는 구조를 따르고, Rove가 더하는 것은 다음과 같다. 생성되는 디렉터리(`server/bare`, `server/pd`, `internal/ent`, `ts/gen`, 복사된 payday proto)는 편집하지 않는다.

```text
rove/
├── proto/rove/              엔터티 선언 (proto 패키지 rove)
├── proto/ext/payday/        payday 엔터티 overlay (holder.ext.proto: role)
├── proto/ext/rove/          도메인 RPC overlay (*_svc.ext.proto)
├── server/seal/             외부 호출에 생성 쓰기를 닫는 레이어
├── server/domain/           도메인 RPC, 트랜잭션·잠금·대체 헬퍼, Event 기록
├── server/policy/           역할 Policy (gate.Policy)
├── cmd/rove/                공개 진입점 (테넌트 경계, 자기 테넌트만)
├── cmd/rove-admin/          운영자 진입점 (내부망)
├── migrations/ent/          ent Plan 결과 (리뷰 후 커밋)
├── migrations/pg-extra/     직접 쓰는 PostgreSQL DDL
├── deploy/postgres/         pg_bigm을 넣은 PostgreSQL 18 이미지 (개발·CI·운영 공용)
├── deploy/compose/          self-host 구성 (리버스 프록시, rove, rove-admin, PostgreSQL, 백업)
├── ts/                      React PWA
├── scripts/                 with-postgres, seed
├── design.md
└── plan.md
```

## 4. 엔터티와 도메인 번호

pdid 도메인 번호는 한 번 정하면 바꾸거나 재사용하지 않는다. payday가 1~6을 쓰고(Tenant 1, Holder 2, Audit 3, Outbox 4), Rove는 7부터 아래 순서로 만든다. Phase 2 이후의 엔터티는 만드는 순서대로 25부터 이어 간다.

| 엔터티 | 도메인 | Phase | Erase | watch | 외부에 여는 생성 RPC |
| --- | --- | --- | --- | --- | --- |
| `Asset` | 7 | 0 | soft | ✓ | Add(완성: 등록), Get, List, Watch, Erase(완성: 오입력 취소) |
| `AssetType` | 8 | 0 | soft | | Add, Get, List, Erase + `Update` |
| `ItemModel` | 9 | 0 | soft | | Add, Get, List, Erase + `Update` |
| `Party` | 10 | 0 | soft | | Add, Get, List, Erase + `Update`, `Pseudonymize` |
| `Identity` | 11 | 0 | hard | | 만들지 않는다. IdP 계정의 대응은 roster가 갖는다(design 9.10절). 번호는 비워 둔다 |
| `TreeLock` | 12 | 0 | hard | | 없음 |
| `Placement` | 13 | 0 | hard (보존 잡) | | 없음 (이력 RPC로만 읽는다) |
| `Link` | 14 | 0 | hard (보존 잡) | | 없음 |
| `Stewardship` | 15 | 0 | hard (보존 잡) | | 없음 |
| `Fact` | 16 | 0 | hard (보존 잡) | | 없음 |
| `Event` | 17 | 0 | hard (보존 잡) | | 없음 (`Timeline`으로 읽는다) |
| `Label` | 18 | 0 | soft | | Get + `Print`, `Bind`, `Unbind`, `Resolve` (테넌트 라벨 도메인이 있을 때만) |
| `Attachment` | 19 | 0 | soft | | Get, List |
| `UsageSnapshot` | 20 | 0 | hard | | 운영자 스택에서만 |
| `Custody` | 21 | 1 | soft | ✓ | Add(완성: 지급), Get, List, Watch |
| `CustodyLine` | 22 | 1 | hard | | Get, List |
| `SpaceProfile` | 23 | 1 | hard | | Get + `Update` |
| `TenantDomain` | 24 | 1 | soft | | Add, Get, List + `Verify`, `Activate`, `Retire` |

- "외부에 여는 생성 RPC"에 없는 생성 메서드는 `seal`이 닫는다. "(완성)"은 도메인 레이어가 생성 동사를 완성한 것이고, 그 밖의 상태 변경은 새 도메인 RPC다(design 6장·9.8절).
- 테넌트 행을 가리키는 엣지는 불변으로 두고 `agrees`에 선언한다. 바뀌는 참조(`Asset.parent`, `custodian`, `type`, `model`, `Party.parent`, `Party.account`)에는 pg-extra에 테넌트 일치 트리거를 둔다.

## 5. 마일스톤

MVP(Phase 0 + 1)는 M0~M5이고, 합쳐서 대략 13~16주로 본다.

### M0 — 기반 (약 1주)

- devcontainer: 이름 오타는 고쳤다(`lesomnus/rove`). `deploy/postgres` 이미지로 PostgreSQL 서비스를 붙이고(payday처럼 docker-in-docker를 써도 된다), `scripts/with-postgres.sh`를 둔다.
- `pd new`로 골격을 만들고, proto 패키지를 `rove`로, payday 버전을 고정한다.
- 마이그레이션 적용 명령: ent 디렉터리 다음 pg-extra 디렉터리, 그 뒤 `entschema.Check`.
- CI 골격(7장).
- **완료 기준**: 빈 앱이 PostgreSQL에서 뜨고 CI가 녹색이다. 새 DB에 두 디렉터리를 적용한 뒤 Check가 통과하고, Plan이 아무 파일도 만들지 않는다.

### M1 — 시간 코어 (약 3~4주)

- 엔터티: Asset, AssetType, ItemModel, Party, TreeLock, Placement, Link, Stewardship, Fact, Event.
- `server/domain`: 트랜잭션 헬퍼(`BeginTx` + `Rebind`), 잠금 헬퍼(테넌트 잠금 행, id 순서), 대체 후 삽입 헬퍼, Event 기록, Event ID 기반 멱등성.
- `server/seal`.
- 생성 동사 완성: 자산 `Add`(초기 배치·Fact·Event와 함께 등록), 자산 `Erase`(열린 시간 행을 대체하는 오입력 취소).
- 새 도메인 RPC: Move, Install, Remove, Assign, Unassign, SetAttributes, ChangeType, Correct.
- 이력 RPC: Timeline, QueryAt, AsOf, Diff (조회 창 적용).
- pg-extra: btree_gist, `EXCLUDE`(물리 부모 하나, 슬롯, 랙 U 범위, 담당 역할), Fact 부분 유일 인덱스, 테넌트 일치 트리거.
- **완료 기준**
  - 시간 오라클: 소급 기록과 정정을 섞은 무작위 작업 열을 만들고, `QueryAt(T)`·`AsOf(T, K)`가 단순 재생 모델과 같은지 속성 기반 테스트(`pgregory.net/rapid`)로 확인한다.
  - 동시성: 같은 자산의 동시 이동 N건 중 하나만 성공한다. V10의 순환 경쟁은 거부되고, 같은 슬롯의 동시 장착은 하나만 성공한다.
  - 교차 테넌트: 모든 RPC에 다른 테넌트의 ID를 넣으면 NotFound 또는 InvalidArgument가 된다. 도메인 레이어를 거치지 않은 SQL은 트리거가 23503으로 거부한다.
  - SQLite(DB 제약 없음)와 PostgreSQL 모두 통과한다.
  - 성능: 자산 10만 개 시드에서 1,000노드 하위 트리 `QueryAt`의 p95가 1초 미만이다.

### M2 — 계정·권한·운영자 (약 2주)

- 신원은 roster다(design 9.10절, D26). 프로토타입의 비밀번호·DB 세션(report 결정 3)을 세 단계로 옮긴다.
  1. **roster에 붙기**: `internal/identity` 하나가 외부 roster(TLS, `rk_`/`rt_`)와 내장 roster(`bufconn`)를 같은 인터페이스로 감싼다. `auth.roster` 설정. Tenant·Holder를 roster의 ID에 고정해 처음 들어올 때 만든다. 비밀번호 로그인은 `VouchService.Verify`로, 세션을 쓰는 호출마다 roster에 사람을 확인한다(짧은 캐시, fail closed). `rove init`과 새 `rove holder add`는 내장 roster에 쓴다. 사람 화면의 로그인 발급·비밀번호 재설정·로그인 중지는 roster 호출이 된다. `Credential`과 `server/password`를 지운다. 기존 배포는 `rove identity migrate`.
  2. **SSO**: roster issuer의 relying party(`/sso/login`, `/sso/callback`, `/sso/logout`), `auth.sso_only`, 로그인 화면의 SSO 버튼.
  3. **생명주기**: `SyncService/Watch`를 따라가 roster의 중지·모든 곳에서 로그아웃은 세션 종료로, 사람 삭제는 가명화(Party·trail)로 이어 간다.
- 로그인 단계의 테넌트 선택(테넌트 alias 경로 또는 서브도메인).
- Holder overlay `role`, 메서드별 최소 역할 표로 Policy 구현. 같은 Policy를 batch guard에 넘긴다.
- 공개·운영자 바이너리 분리, 테넌트의 첫 행이 생길 때 유형 템플릿 복사.
- **완료 기준**
  - 역할 × 메서드 표 전체를 테스트한다.
  - `member`가 batch 안에 관리자 작업을 끼워 넣지 못한다.
  - 공개 스택은 여러 테넌트를 보는 자격 증명으로도 다른 테넌트 행을 돌려주지 않는다.
  - 같은 신원 테스트가 내장 roster와 외부 roster(TCP 리스너와 키) 양쪽에서 통과한다.
  - roster에서 중지된 사람의 세션은 캐시 시간 안에 거부되고, roster에 물을 수 없으면 세션을 쓰는 호출은 `UNAVAILABLE`이다.
  - Rove DB에 비밀번호도 그 검증값도 없다.

### M3 — 웹 UI·QR·가져오기 (약 3~4주)

- React PWA: 자산 목록·상세·타임라인·시점 슬라이더·Diff, 공간 트리, 검색(`Search` RPC, pg_bigm).
- TenantDomain(design 9.9절):
  - 자체 도메인과 Rove 기본 하위 도메인 중 선택.
  - 확인용 TXT 토큰 발급과, spin 루프의 DNS 확인.
  - 활성화하면 이전 도메인은 `LEGACY`가 되어 계속 해석된다.
  - 운영자 진입점에 on-demand TLS 확인 엔드포인트를 둔다.
  - 공개 진입점에 Host 기준 라벨 리다이렉트를 둔다.
- Label: 인쇄(PDF 시트), Bind·Unbind·Resolve. 확인된 라벨 도메인이 없으면 화면에서 숨기고 RPC는 `FailedPrecondition`으로 답한다. 카메라 스캔은 `BarcodeDetector`를 쓰고, 지원하지 않는 브라우저는 JS 디코더로 넘긴다.
- 과거 시각을 포함한 CSV/Excel 가져오기와 내보내기.
- Attachment: 저장소 인터페이스와 로컬 파일시스템 구현, 앱이 서명한 다운로드 URL, sha256, 악성 파일 검사 연결 지점.
- 뷰 플러그인 슬롯과 랙 U·하드웨어 구성 뷰.
- **완료 기준**
  - 가져오기 직후 과거 시점이 재현된다.
  - 휴대폰 기본 카메라로 라벨을 찍으면 테넌트가 선택된 채 자산이 열린다.
  - 도메인이 없는 테넌트에는 라벨 기능이 보이지 않는다.
  - 도메인을 바꾼 뒤에도 이전 도메인으로 인쇄한 라벨이 열린다.
  - 확인되지 않은 도메인에는 TLS 인증서가 발급되지 않는다.
  - 2음절 한글 검색이 인덱스를 쓴다.
  - 주요 흐름의 Playwright e2e가 통과한다.

### M4 — 지급·반납 (약 2주)

- Custody·CustodyLine. Custody `Add`(완성: 지급), Acknowledge, Return, Extend가 `CUSTODIAN` Stewardship을 열고 닫는다.
- 부분 반납, 셀프서비스(스캔 → 인수 확인), 연체 계산(알림은 Phase 2).
- **완료 기준**: 지급 → 반납 → 정정 시나리오의 `AsOf` 결과가 기대와 같다. 부분 반납과 `member` 셀프서비스 e2e가 통과한다.

### M5 — MVP 마감과 파일럿 (약 2주)

- `UsageSnapshot` spin 루프.
- trail 보존 설정: Holder는 `pipa` 이상(`audit.min` 또는 `audit.by`), 이력 도메인은 조직의 계약이 정하는 보존 기간(design 8.2절, `server/retention`).
- self-host 운영 환경(`deploy/compose`):
  - 서버 준비, 리버스 프록시(ACME, 테넌트 도메인용 on-demand TLS), 공개·운영자 진입점, PostgreSQL.
  - 운영자 진입점은 VPN에서만 닿게 한다.
- 백업: pgBackRest(또는 WAL-G)로 WAL 보관 PITR을 하고, 첨부 디렉터리 백업과 함께 다른 장소로 보낸다. 복구 리허설로 RPO·RTO를 측정한다.
- 관측: OTel 수집기와 대시보드, 디스크·백업·인증서 만료 경보.
- 스테이징(같은 compose를 다른 호스트나 포트에)과 운영 배포 절차.
- 보안 점검: payday 권한 가이드 13절 체크리스트, Rove 항목, 서버 하드닝(자동 보안 업데이트, 방화벽 80/443, 디스크·백업 암호화).
- 파일럿 고객 온보딩(기존 스프레드시트 가져오기).
- **완료 기준**: 파일럿 테넌트가 실제로 쓰고 있다. 복구 리허설을 마쳤고, design 1.2절의 비기능 목표 측정값을 기록했다.

### Phase 2 이후 (개요)

MVP 이후에 파일럿 피드백으로 순서를 다시 정한다.

- **M6 예약**: Bookable, Allocation, 홀드·승인·buffer·override, 공간 배타 그룹, `Availability` RPC, 캘린더 watch 신호.
- **M7 키트와 풀 자원**: 키트 구성과 미래 예약 재검증, 모델 단위 예약.
- **M8 실사와 재고**: 오프라인 큐(클라이언트가 발급한 pdid, `occurred_at`, 멱등 `Scan` RPC), Stock·StockMovement.
- **M9 운영**: 작업 지시·정비 블록, 구매, 알림, 리포트, 반복 예약.
- **M10 도메인 모듈 구조.**
- **Phase 3**: SAML·SCIM, 사업장 단위 권한(필드 3), API 토큰·Webhook, ICS, 보존·삭제 잡과 개인정보 삭제, 사용량 원장·과금, 서드파티 플러그인, RFID·GPS, 회계 내보내기.

## 6. 테스트 전략

| 층 | 대상 | DB |
| --- | --- | --- |
| 단위 | 도메인 계산(구간 분할, 대체 계획, 권한 표) | 없음 또는 SQLite |
| 통합 | 도메인 RPC, 제약, 트리거, 마이그레이션 | PostgreSQL |
| 속성 기반 | 시간 오라클(`QueryAt`, `AsOf`, `Diff`) | PostgreSQL |
| 동시성 | 이동·장착·예약 경쟁 | PostgreSQL |
| 교차 테넌트 | 생성 서비스 디스크립터를 돌며 모든 RPC에 다른 테넌트 ID 주입 | PostgreSQL |
| e2e (M3부터) | 주요 화면 흐름 | PostgreSQL |

- 시간 오라클의 모델은 (효력 시각, 기록 시각, 대체 여부)를 가진 단언 목록이다. 무작위로 고른 T, K에서 SQL 조회 결과와 비교한다.
- payday 관례를 따른다: PostgreSQL 실행은 `with-postgres` 스크립트로, 골든 파일은 `pdtest`로, 결과는 출력을 grep하지 않고 종료 코드로 판단한다.

## 7. CI

| 작업 | 내용 |
| --- | --- |
| `go` | gofmt, vet, 테스트 (SQLite) |
| `go-pg` | `deploy/postgres` 이미지를 서비스로 띄우고 같은 테스트 + PostgreSQL 전용 테스트 |
| `gen` | `go tool pd gen --check --ts .` |
| `buf` | lint, main 대비 breaking |
| `migrate` | 새 DB에 ent → pg-extra 적용, `entschema.Check` 통과, dev DB에 Plan을 돌려 새 파일이 없는지 확인 |
| `ts` | 타입 검사, vitest, Playwright(M3부터) |

배포(M5부터): 컨테이너 이미지를 GHCR에 올리고, 스테이징 배포는 수동 승인으로 한다.

## 8. 개발 규칙

- **도메인 RPC 체크리스트**
  1. 트랜잭션 헬퍼로 시작한다.
  2. 잠금은 테넌트 잠금 행이 먼저, 그다음 자원·자산 행을 id 순서로 잡는다.
  3. 대상은 테넌트 경계를 통해 읽는다.
  4. 기존 시간 행을 먼저 대체하고, 그다음 새 행을 넣는다. 생성 동사를 완성할 때는 본 행을 아래(`next`)로, 추가 행을 이 레이어를 다시 묶은 것(`at`)으로 쓴다.
  5. 변경이 '지금'에 영향을 주면 Asset 현재 상태를 갱신하고, 컨테이너·자원 행의 버전을 올린다.
  6. Event를 기록한다(클라이언트가 발급한 ID가 멱등 키).
  7. 커밋한다.
- **스키마**: payday 필드 번호 규칙(1·2·4~7·13~15는 payday, 3은 사업장 축으로 비워 둠)과 4장의 도메인 번호를 따른다. 테넌트 행을 가리키는 엣지는 불변 + `agrees`, 바뀌는 참조는 트리거를 함께 추가한다.
- **마이그레이션**: ent 마이그레이션은 Plan으로만 만들고 리뷰한다. pg-extra는 직접 쓰고 `atlas.sum`을 갱신한다. 새 파일의 버전은 두 디렉터리를 통틀어 마지막 적용 버전보다 뒤여야 하므로, 브랜치를 합칠 때 오래된 파일은 다시 만든다.
- **문서**: 설계 결정이 바뀌면 design.md 12장을 먼저 고치고, 일정·순서가 바뀌면 이 문서를 고친다.

## 9. 위험과 대응

| 위험 | 영향 | 대응 |
| --- | --- | --- |
| payday 변경(`dev` 라벨, protobuf-orm과 함께 바뀜) | 재생성·마이그레이션이 깨진다 | 버전 고정, CI의 `pd gen --check`, 업그레이드는 별도 작업으로 |
| 시간 모델의 복잡도 | 버그와 지연 | M1에서 오라클 테스트를 먼저 만들고, UI 전에 API를 굳힌다 |
| SQLite에서만 통과 | 운영 장애 | 도메인 테스트는 PostgreSQL 필수 |
| 관리형 DB로 옮길 때 pg_bigm이 없는 경우 | 검색 품질 저하 | `Search` RPC 뒤에 폴백(design 9.5절) |
| 테넌트 잠금 행 경합 | 대형 테넌트의 이동 지연 | 계측하고, 필요하면 사업장 단위 잠금으로 나눈다 |
| 1인 개발 범위 | 일정 초과 | MVP 범위를 고정하고, Phase 2부터는 파일럿 피드백으로 다시 정한다 |
| 서버 한 대(self-host) | 하드웨어 고장이 곧 장애 | 다른 장소의 PITR 백업, 분기 복구 리허설, compose로 재구축하는 절차 문서 |
| 운영 부담(보안 업데이트, 인증서, 디스크) | 사고와 장애 | 자동 보안 업데이트, 인증서 만료·디스크·백업 실패 경보 |
| 테넌트 도메인 인증서 남발 | ACME 발급 한도 소진 | 확인된 도메인에만 발급(on-demand TLS 확인 엔드포인트), 인증서 저장소 보존 |
| Rove 기본 도메인 변경 | 기본 하위 도메인으로 인쇄한 라벨과 OIDC 등록이 깨진다 | 처음에 영구 도메인을 정하고 바꾸지 않는다 |

## 10. 정해야 할 것

정한 것: 인프라는 self-host(design D21), QR 라벨 도메인은 테넌트마다 설정(D23), 첨부는 로컬 파일시스템으로 시작(D25).

| 결정 | 언제까지 | 내용 |
| --- | --- | --- |
| 저장소 공개 여부와 라이선스 | 코드를 올리기 전(M0) | 지금 공개 저장소이고 LICENSE가 없다. 상용 SaaS의 소스를 공개할지, 공개한다면 어떤 라이선스인지 |
| Rove 기본 도메인 | M2 전 | 앱 호스트, OIDC 리다이렉트 주소, 라벨 CNAME 대상, 기본 하위 도메인. 한 번 정하면 바꾸지 않는다 |
| 운영 서버와 백업 위치 | M5 전 | 서버를 어디에 둘지(자체 장비, 코로케이션, VPS), 백업을 보낼 다른 장소 |
| 역할의 출처 | M2 단계 1 전 | Rove의 `Holder.role`과 정책 표(지금, 기본)로 둘지, roster의 역할(메서드 묶음)을 `HolderService/Reaches`로 읽을지(design 9.10절). 뒤쪽은 SCIM 그룹으로 권한을 주는 기업에 맞지만, 권한 화면이 roster 콘솔로 간다 |
| 테넌트를 만드는 사람과 탈퇴 순서 | M2 단계 1 전 | 외부 roster에서 새 조직을 누가 만드는지(roster 운영자의 `roster tenant add`와 `roster app install rove`로 시작), 셀프 가입을 둘지, 탈퇴 때 Rove의 내보내기·전체 삭제와 roster 테넌트 삭제의 순서 |
| 사람 삭제의 방향 | M2 단계 3 전 | roster에서 지운 사람을 Rove가 따라 가명화하는 것은 정했다. Rove 사람 화면의 '개인정보 삭제'가 roster의 사람도 지울지 |
| 옮길 기존 데이터 | M2 단계 1 | 지금 배포에 옮길 사람이 있는지. 있으면 `rove identity migrate`로 옮기고 비밀번호는 새로 발급한다 |
| 파일럿 | M3 중 | 아래 |

**파일럿에서 정할 것**: 파일럿은 MVP를 실제 업무에 4~8주 써 보는 첫 조직이고, design 1.1절의 가설을 검증하는 대상이다.

- **대상**: 조직 1~2곳. 연구실, 스튜디오, 메이커스페이스, 또는 내가 속한 팀(직접 써 보기)도 된다.
- **범위**: 자산 수(수백 개 규모), 사용자 수, 쓸 기능(등록, QR, 지급·반납, 이력 조회).
- **일정**: M3 중에 실제 데이터를 받아 가져오기 형식을 맞추고, M5에 시작한다.
- **성공 기준**: 예를 들어 4주 안에 자산의 80%에 라벨 부착, 지급·반납을 Rove로만 처리, "그때 어디 있었나"에 실제로 답한 사례.
- **데이터와 동의**: 지금 쓰는 스프레드시트 사본, 개인정보 처리 동의(서버 위치와 보관 기간 명시).
- **라벨 도메인**: 조직이 DNS를 바꿀 수 있는지. 못 바꾸면 Rove 기본 하위 도메인을 쓴다.
- **피드백**: 연락 창구와 피드백 주기.

## 11. payday로 올릴 것

Rove에서 우회하고 있지만 payday에서 고치면 우회가 필요 없어지는 것들이다. 2026-10-10에 이슈로 올렸다.

| 이슈 | 내용 | Rove의 우회 |
| --- | --- | --- |
| [#32](https://github.com/lesomnus/payday/issues/32) | Gate의 엣지 확인에 관한 문서 네 곳이 코드와 다르다 | 코드 기준으로 설계(6장) |
| [#33](https://github.com/lesomnus/payday/issues/33) | `entschema.Check`가 ent 밖 FK를 거부하고, Plan이 같은 디렉터리의 ent 밖 객체를 지우려 한다 | pg-extra 디렉터리, 복합 FK 대신 트리거 |
| [#34](https://github.com/lesomnus/payday/issues/34) | 앱 레이어는 부를 수 있고 호출자는 못 부르는 생성 동사를 선언할 방법이 없다 | `seal` 레이어 |
| [#35](https://github.com/lesomnus/payday/issues/35) | trail 보존에 테넌트 차원이 없고, DB의 trail에서 특정인 정보를 지울 수 없다 | 해결됨: payday가 테넌트별로 묻고(`trail.Policy.Tenants`), Rove는 계약과 법적 보존으로 답한다(`server/retention`) |
| [#36](https://github.com/lesomnus/payday/issues/36) | overlay로 payday 엔터티에 인덱스를 더할 수 없고, 거부 없이 버려진다 | `Identity` 엔터티를 따로 두려 했으나, IdP 계정의 대응이 roster로 가서 필요 없어졌다(design 9.10절) |
| [#37](https://github.com/lesomnus/payday/issues/37) | 클라이언트 store에 오프라인 쓰기 큐가 없다 | Phase 2에 직접 구현 |

처음 목록에 있던 "여러 행을 쓰는 RPC의 트랜잭션"은 이슈로 올리지 않았다. 최신 payday main의 서버 가이드에 "completing a generated verb"로 문서화되어 있고(`1308774`), `composite_test.go`가 그 패턴을 확인한다.
