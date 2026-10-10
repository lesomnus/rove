# Rove

물리 자산과 공간을 관리하는 멀티 테넌트 서비스입니다. 무엇이 어디에 있었고 누가 가지고 있었는지를 **시간을 따라** 기록하고, 공간과 장비의 예약, 지급·대여, 재고, 실사, 정비, 구매를 한곳에서 다룹니다.

이 브랜치(`prototype`)는 파일럿을 위한 **localhost 프로토타입**입니다. 설계는 [`design.md`](design.md), 개발 계획은 [`plan.md`](plan.md), 프로토타입을 만들며 내린 주요 결정은 [`report.md`](report.md)에 있습니다.

> 이 저장소는 공개되어 있지만 오픈소스가 아닙니다. [`LICENSE`](LICENSE)를 보세요.

## 바로 실행하기

필요한 것: Go 1.27, Node 22+ (UI를 빌드할 때만). 데이터베이스는 기본이 SQLite 파일이라 따로 설치할 것이 없습니다.

```sh
# UI 빌드 (한 번)
cd ts && npm install && npm run build && cd ..

# 첫 조직과 소유자, 그리고 체험용 데이터(5개 팀 50명, 공간, 자산 140여 개)
go run ./cmd/rove init --tenant rove --name "우리 회사" --login admin --password admin1234 --demo

# 서버
go run ./cmd/rove serve
```

<http://localhost:8080> 에서 `admin` / `admin1234` 로 로그인합니다. `--demo` 로 만든 사람들은 모두 비밀번호 `demo1234` 로 로그인할 수 있고, `init` 이 역할별 예시 아이디를 출력합니다 (예: 매니저 `minjun-kim`, 관리자 `gunwoo-lee`, 감사자 `naeun-park`, 구성원 `haeun-lee`).

체험용 데이터 없이 실제로 쓰려면 `--demo` 를 빼고, `--password` 를 빼면 비밀번호를 만들어 출력합니다. 처음부터 다시 하려면 `data/` 디렉터리를 지웁니다.

### UI 개발

```sh
go run ./cmd/rove serve          # :8080
cd ts && npm run dev             # :5173, API 호출은 Vite가 :8080으로 넘김
```

스캔한 라벨이 개발 서버로 열리게 하려면 `ROVE_APP_APP_URL=http://localhost:5173` 을 주고 서버를 띄웁니다.

### PostgreSQL로 실행

배포 데이터베이스는 PostgreSQL입니다. SQLite에는 없는 안전장치(예약·위치가 겹치면 DB가 거부하는 EXCLUDE 제약 등)가 `serve` 의 마이그레이션 때 함께 설치됩니다.

```sh
docker run -d --name rove-pg -e POSTGRES_USER=rove -e POSTGRES_PASSWORD=rove -p 5432:5432 postgres:18
ROVE_DB_DRIVER=pgx ROVE_DB_DSN='postgres://rove:rove@localhost:5432/rove?sslmode=disable' go run ./cmd/rove init --demo
ROVE_DB_DRIVER=pgx ROVE_DB_DSN='postgres://rove:rove@localhost:5432/rove?sslmode=disable' go run ./cmd/rove serve
```

설정할 수 있는 모든 값은 `go run ./cmd/rove config env` 로 볼 수 있습니다. 기본값은 [`rove.yaml`](rove.yaml)에 있습니다.

### 휴대폰으로 스캔하려면

브라우저는 **HTTPS 이거나 localhost 일 때만** 카메라를 열어 줍니다. 그래서 PC의 `http://localhost:8080` 은 그 PC에서만 카메라 스캔이 됩니다.

- 같은 PC에서 USB/블루투스 바코드 스캐너를 쓰면 스캔 화면의 입력칸에 그대로 입력됩니다 (QR 주소와 태그 모두 읽습니다).
- 휴대폰에서 쓰려면 서버를 HTTPS로 열어야 합니다. 예를 들어 [Caddy](https://caddyserver.com)를 앞에 두고(`reverse_proxy localhost:8080`, 사내망이면 `tls internal`) `app.public_url` 과 `app.labels.suffix` 를 그 주소에 맞춥니다. 라벨 QR은 `https://<조직>.<suffix>/l/<코드>` 로 인쇄되므로, 휴대폰 기본 카메라로 찍어도 바로 그 자산이 열립니다.

### 조직별 이력 보존 (운영자)

조직마다 이력을 얼마나 뒤까지 보여 주고 얼마나 보관할지는 그 조직의 **계약**이 정합니다(design 8). 계약과 법적 보존(hold)은 운영자의 것이라 셸에서만 씁니다. 조직의 사람은 읽기만 합니다.

```sh
# 내년 1월부터: 1년치를 보여 주고 2년치를 보관
go run ./cmd/rove contract set --tenant rove --name free --view 365 --keep 730 --effective 2027-01-01
# 줄어드는 보관 기간은 유예(--grace, 일)가 지나야 적용됩니다. 보기 기간은 바로 줄어듭니다
go run ./cmd/rove contract set --tenant rove --name trial --view 90 --keep 180 --grace 30
go run ./cmd/rove contract show --tenant rove   # 계약들과 지금 적용되는 기간, 걸린 hold

go run ./cmd/rove hold place --tenant rove --why "사건 2026-1"   # 이력을 아무것도 지우지 않음
go run ./cmd/rove hold lift <hold-id>
```

**지우는 일은 기본으로 꺼져 있습니다.** 고지·내보내기·유예·백업 정책이 정해지기 전에는 파괴적인 동작을 켜지 않는다는 원칙(design 1장) 때문입니다. `app.retention.apply: true` 를 주어야 보관 기간이 지난 감사 기록(이력 종류)이 지워집니다. 계약이 없는 조직은 `app.retention.view`·`keep` 을 따르고, 그것도 없으면 전부 보여 주고 전부 보관합니다.

## 무엇이 있나

| 화면 | 할 수 있는 일 |
| --- | --- |
| 대시보드 | 현황 숫자, 내 자산·예약, 승인 대기, 연체, 재고 부족, 알림 |
| 자산 | 검색·필터, 등록, 엑셀/CSV 가져오기(미리 보기)·내보내기, 라벨 인쇄 |
| 자산 상세 | 이동(지난 일은 그 시점으로), 정보·상태 변경, 소유·관리 담당, 키트·그룹 연결, 지급, 고장 신고, 첨부, 라벨, **이력과 정정**, 플러그인(랙 배치도, 보증) |
| 공간 | 공간 트리, **과거 어느 시점의 모습**, 두 시점 사이 변화 |
| 예약 | 주간 캘린더, 반복 예약, 임시 홀드, 승인, 체크인, 노쇼 자동 해제, 장비 수령 |
| 지급·대여 | 지급/대여, 인수 확인, 부분 반납, 기한 연장, 연체 알림 |
| 재고 | 입고·사용·조정·이동, 자산으로 전환, 부족 알림, 내역 |
| 실사 | 범위 지정, 카메라/스캐너로 스캔(오프라인이면 모아 두었다 전송), 대조, 위치 반영·분실 처리 |
| 작업 | 수리·점검·정비, 예약 막기, 정기 점검 반복, 구성원의 고장 신고 |
| 구매 | 주문, 입고 시 자산이나 재고로 등록 |
| 사람 | 조직도, 로그인 발급, 역할, 비밀번호 재설정, 로그인 중지, 개인정보 삭제 |
| 설정 | 유형과 속성, 모델, 예약 자원, 라벨 도메인, 작업 기록, 감사 기록, 사용량 |

역할: **소유자 · 관리자 · 매니저 · 구성원 · 감사자**. 무엇을 누가 할 수 있는지는 [`server/policy/policy.go`](server/policy/policy.go) 의 표 하나가 정하고, 표에 없는 것은 거부됩니다.

## 구조

```
proto/rove/*.proto          엔터티 (스키마의 원천)
proto/ext/rove/*.ext.proto  엔터티에 더한 RPC
server/domain/              도메인 계층: 시간 기록, 예약 충돌, 지급, 재고, 실사 … 그리고 백그라운드 작업
server/policy/              역할 → 호출할 수 있는 RPC
server/session/             로그인(argon2id)과 DB에 두는 세션
server/storage/             첨부 파일과 서명된 다운로드 주소
cmd/, cli/                  서버 조립, init, serve
ts/src/                     React UI
```

대부분은 [payday](https://github.com/lesomnus/payday)가 `proto/`에서 생성합니다. 스키마를 고쳤다면:

```sh
go tool pd gen .          # Go
go tool pd gen --ts .     # TypeScript
go tool pd gen --check .  # 생성물이 스키마와 같은지 (CI가 하는 일)
```

생성 파일(`*.g.go`, `*.pb.go`, `server/bare/`, `server/pd/`, `internal/ent/`, `ts/gen/`)은 직접 고치지 않습니다. 자세한 규칙은 [`CLAUDE.md`](CLAUDE.md).

## 테스트

```sh
go test ./...                                                    # SQLite
PDTEST_POSTGRES='postgres://rove:rove@localhost:5432/rove?sslmode=disable' go test ./...   # PostgreSQL
cd ts && npm run check                                           # 타입 검사
```

도메인 테스트는 실제 호출과 같은 경로(게이트, 테넌트 벽, 도메인 계층)를 지나고, `cmd` 테스트는 gRPC 체인(세션 쿠키, 역할 표, 배치) 전체를 지납니다.
