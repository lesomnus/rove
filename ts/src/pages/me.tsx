import { useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'

import { AssetService, CustodyService, PartyService, fmt, idStr, ref } from '../api.js'
import { useCatalog } from '../catalog.js'
import { AssetLink } from '../pickers.js'
import { useAct, useRpc } from '../rpc.js'
import { Badge, Card, Empty, Field, FormModal, Kv, Load, PageHead } from '../ui.js'
import { custodyKindWord, roleWord, statusWord } from '../words.js'

export function Me(): ReactNode {
	const c = useCatalog()
	const act = useAct()
	const [pw, setPw] = useState(false)
	const p = c.me.party
	const mine = useRpc(AssetService.method.search, p === undefined ? null : { custodian: ref(p.id), size: 200 })
	const custodies = useRpc(CustodyService.method.list, p === undefined ? null : { filters: [{ party: ref(p.id) }], size: 200 })

	return (
		<>
			<PageHead title="내 정보" actions={<button onClick={() => setPw(true)}>비밀번호 변경</button>} />
			<div className="grid2">
				<Card title="계정">
					<Kv
						items={[
							['이름', p?.name ?? c.me.holder?.name ?? '-'],
							['아이디', c.me.holder?.alias ?? '-'],
							['이메일', p?.email || '-'],
							['소속', c.party(p?.parentId)?.name ?? '-'],
							['역할', roleWord[c.role] ?? c.role],
							['조직', c.me.tenant?.name || c.me.tenant?.alias || '-'],
						]}
					/>
				</Card>
				<Card title="인수 확인이 필요한 것">
					<Load q={custodies}>
						{(d) => {
							const waiting = d.items.filter((x) => x.status === 'open' && x.acknowledgedAt === undefined)
							return waiting.length === 0 ? (
								<Empty>없습니다.</Empty>
							) : (
								<ul className="rows">
									{waiting.map((x) => (
										<li key={idStr(x.id)}>
											<Link to={`/custody/${idStr(x.id)}`}>
												{custodyKindWord[x.kind]} · {fmt(x.issuedAt, 'date')} {x.desc}
											</Link>
											<button className="primary small" onClick={() => void act(CustodyService.method.acknowledge, { ref: ref(x.id) }, { ok: '확인했습니다.' })}>
												받았습니다
											</button>
										</li>
									))}
								</ul>
							)
						}}
					</Load>
				</Card>
				<Card title="가지고 있는 자산">
					<Load q={mine}>
						{(d) =>
							d.items.length === 0 ? (
								<Empty>없습니다.</Empty>
							) : (
								<ul className="rows">
									{d.items.map((a) => (
										<li key={idStr(a.id)}>
											<AssetLink a={a} />
											<Badge v={a.status} words={statusWord} />
										</li>
									))}
								</ul>
							)
						}
					</Load>
				</Card>
				<Card title="대여 중">
					<Load q={custodies}>
						{(d) => {
							const loans = d.items.filter((x) => x.status === 'open' && x.kind === 'loan')
							return loans.length === 0 ? (
								<Empty>없습니다.</Empty>
							) : (
								<ul className="rows">
									{loans.map((x) => (
										<li key={idStr(x.id)}>
											<Link to={`/custody/${idStr(x.id)}`}>{x.desc || '대여'}</Link>
											<span>반납 {fmt(x.dueAt)}</span>
										</li>
									))}
								</ul>
							)
						}}
					</Load>
				</Card>
			</div>
			{pw && <ChangePassword onClose={() => setPw(false)} />}
		</>
	)
}

function ChangePassword(props: { onClose: () => void }): ReactNode {
	const act = useAct()
	const [current, setCurrent] = useState('')
	const [next, setNext] = useState('')
	const [again, setAgain] = useState('')
	return (
		<FormModal
			title="비밀번호 변경"
			submit="변경"
			onClose={props.onClose}
			onSubmit={async () => {
				if (next !== again) return false
				return (await act(PartyService.method.setPassword, { current, password: next }, { ok: '바꿨습니다.' })) !== undefined
			}}
		>
			<Field label="지금 비밀번호">
				<input type="password" value={current} onChange={(e) => setCurrent(e.target.value)} required autoComplete="current-password" />
			</Field>
			<Field label="새 비밀번호" hint="8자 이상">
				<input type="password" value={next} onChange={(e) => setNext(e.target.value)} required minLength={8} autoComplete="new-password" />
			</Field>
			<Field label="새 비밀번호 확인">
				<input type="password" value={again} onChange={(e) => setAgain(e.target.value)} required minLength={8} autoComplete="new-password" />
			</Field>
			{again !== '' && next !== again && <p className="bad wide">새 비밀번호가 서로 다릅니다.</p>}
		</FormModal>
	)
}
