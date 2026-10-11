/**
 * The handful of pieces every page is built from.
 *
 * @module
 */

import {
	createContext,
	useCallback,
	useContext,
	useEffect,
	useRef,
	useState,
	type ReactNode,
} from 'react'

import { errorText } from './api.js'
import { tone, word } from './words.js'

export function Badge(props: { v: string; words?: Record<string, string>; children?: ReactNode }): ReactNode {
	return <span className={`badge ${tone(props.v)}`}>{props.children ?? (props.words ? word(props.words, props.v) : props.v)}</span>
}

export function Spinner(): ReactNode {
	return <div className="spinner" aria-label="불러오는 중" />
}

export function Empty(props: { children?: ReactNode }): ReactNode {
	return <div className="empty">{props.children ?? '아직 아무것도 없습니다.'}</div>
}

export function Failed(props: { error: unknown }): ReactNode {
	return <div className="failed">{errorText(props.error)}</div>
}

/** Loading, failure, or what arrived. */
export function Load<T>(props: {
	q: { state: string; data: T | undefined; error: unknown }
	children: (v: T) => ReactNode
}): ReactNode {
	if (props.q.data !== undefined) return props.children(props.q.data)
	if (props.q.state === 'error') return <Failed error={props.q.error} />
	return <Spinner />
}

export function Card(props: { title?: ReactNode; actions?: ReactNode; children?: ReactNode; className?: string }): ReactNode {
	return (
		<section className={`card ${props.className ?? ''}`}>
			{(props.title !== undefined || props.actions !== undefined) && (
				<header>
					<h3>{props.title}</h3>
					<div className="actions">{props.actions}</div>
				</header>
			)}
			{props.children}
		</section>
	)
}

export function PageHead(props: { title: ReactNode; sub?: ReactNode; actions?: ReactNode }): ReactNode {
	return (
		<div className="page-head">
			<div>
				<h1>{props.title}</h1>
				{props.sub !== undefined && <p className="sub">{props.sub}</p>}
			</div>
			<div className="actions">{props.actions}</div>
		</div>
	)
}

export function Stat(props: { label: string; value: ReactNode; tone?: string; onClick?: () => void }): ReactNode {
	return (
		<div className={`stat ${props.tone ?? ''} ${props.onClick ? 'link' : ''}`} onClick={props.onClick}>
			<div className="value">{props.value}</div>
			<div className="label">{props.label}</div>
		</div>
	)
}

export function Tabs(props: { tabs: { key: string; label: ReactNode }[]; at: string; onChange: (k: string) => void }): ReactNode {
	return (
		<div className="tabs" role="tablist">
			{props.tabs.map((t) => (
				<button key={t.key} role="tab" className={t.key === props.at ? 'on' : ''} onClick={() => props.onChange(t.key)}>
					{t.label}
				</button>
			))}
		</div>
	)
}

export function Field(props: { label: ReactNode; hint?: ReactNode; children: ReactNode; wide?: boolean }): ReactNode {
	return (
		<label className={`field ${props.wide ? 'wide' : ''}`}>
			<span className="label">{props.label}</span>
			{props.children}
			{props.hint !== undefined && <span className="hint">{props.hint}</span>}
		</label>
	)
}

export function Select(props: {
	value: string
	onChange: (v: string) => void
	options: { value: string; label: ReactNode }[]
	empty?: string
	required?: boolean
}): ReactNode {
	return (
		<select value={props.value} onChange={(e) => props.onChange(e.target.value)} required={props.required}>
			{props.empty !== undefined && <option value="">{props.empty}</option>}
			{props.options.map((o) => (
				<option key={o.value} value={o.value}>
					{o.label}
				</option>
			))}
		</select>
	)
}

export function Kv(props: { items: [ReactNode, ReactNode][] }): ReactNode {
	return (
		<dl className="kv">
			{props.items.map(([k, v], i) => (
				<div key={i}>
					<dt>{k}</dt>
					<dd>{v}</dd>
				</div>
			))}
		</dl>
	)
}

/** A dialog over the page; Escape and the backdrop close it. */
export function Modal(props: { title: ReactNode; onClose: () => void; children: ReactNode; wide?: boolean }): ReactNode {
	useEffect(() => {
		const f = (e: KeyboardEvent) => {
			if (e.key === 'Escape') props.onClose()
		}
		window.addEventListener('keydown', f)
		return () => window.removeEventListener('keydown', f)
	}, [props.onClose])

	return (
		<div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && props.onClose()}>
			<div className={`modal ${props.wide ? 'wide' : ''}`} role="dialog" aria-modal="true">
				<header>
					<h2>{props.title}</h2>
					<button className="icon" onClick={props.onClose} aria-label="닫기">
						✕
					</button>
				</header>
				<div className="body">{props.children}</div>
			</div>
		</div>
	)
}

/**
 * A form in a dialog: the fields, and a submit that stays pressed while the
 * write is in flight and closes the dialog when it worked.
 */
export function FormModal(props: {
	title: ReactNode
	submit?: ReactNode
	onClose: () => void
	onSubmit: () => Promise<boolean | void>
	children: ReactNode
	wide?: boolean
	danger?: boolean
}): ReactNode {
	const [busy, setBusy] = useState(false)
	return (
		<Modal title={props.title} onClose={props.onClose} wide={props.wide}>
			<form
				className="form"
				onSubmit={async (e) => {
					e.preventDefault()
					if (busy) return
					setBusy(true)
					try {
						const ok = await props.onSubmit()
						if (ok !== false) props.onClose()
					} finally {
						setBusy(false)
					}
				}}
			>
				<div className="fields">{props.children}</div>
				<footer>
					<button type="button" onClick={props.onClose}>
						취소
					</button>
					<button type="submit" className={props.danger ? 'danger' : 'primary'} disabled={busy}>
						{busy ? '처리 중…' : (props.submit ?? '저장')}
					</button>
				</footer>
			</form>
		</Modal>
	)
}

type Toast = { id: number; text: string; tone: 'ok' | 'bad' | 'info' }
const ToastCtx = createContext<(text: string, tone?: Toast['tone']) => void>(() => {})

export function Toasts(props: { children: ReactNode }): ReactNode {
	const [items, setItems] = useState<Toast[]>([])
	const n = useRef(0)
	const say = useCallback((text: string, tone: Toast['tone'] = 'ok') => {
		const id = ++n.current
		setItems((v) => [...v, { id, text, tone }])
		setTimeout(() => setItems((v) => v.filter((t) => t.id !== id)), tone === 'bad' ? 7000 : 3500)
	}, [])
	return (
		<ToastCtx.Provider value={say}>
			{props.children}
			<div className="toasts">
				{items.map((t) => (
					<div key={t.id} className={`toast ${t.tone}`} onClick={() => setItems((v) => v.filter((x) => x.id !== t.id))}>
						{t.text}
					</div>
				))}
			</div>
		</ToastCtx.Provider>
	)
}

export function useToast(): (text: string, tone?: Toast['tone']) => void {
	return useContext(ToastCtx)
}

/** A button that asks before it does something that cannot be taken back. */
export function Confirm(props: {
	label: ReactNode
	question: ReactNode
	onYes: () => Promise<unknown> | void
	danger?: boolean
	className?: string
	disabled?: boolean
}): ReactNode {
	const [open, setOpen] = useState(false)
	return (
		<>
			<button className={props.className ?? (props.danger ? 'danger' : '')} onClick={() => setOpen(true)} disabled={props.disabled}>
				{props.label}
			</button>
			{open && (
				<FormModal
					title="확인"
					submit={props.label}
					danger={props.danger}
					onClose={() => setOpen(false)}
					onSubmit={async () => {
						await props.onYes()
					}}
				>
					<p>{props.question}</p>
				</FormModal>
			)}
		</>
	)
}

/** A list cut into pages on the client. */
export function usePages<T>(items: T[], size = 50): { page: T[]; at: number; pages: number; go: (n: number) => void } {
	const [at, setAt] = useState(0)
	const pages = Math.max(1, Math.ceil(items.length / size))
	const n = Math.min(at, pages - 1)
	return { page: items.slice(n * size, n * size + size), at: n, pages, go: setAt }
}

export function Pager(props: { at: number; pages: number; go: (n: number) => void }): ReactNode {
	if (props.pages <= 1) return null
	return (
		<div className="pager">
			<button disabled={props.at === 0} onClick={() => props.go(props.at - 1)}>
				이전
			</button>
			<span>
				{props.at + 1} / {props.pages}
			</span>
			<button disabled={props.at >= props.pages - 1} onClick={() => props.go(props.at + 1)}>
				다음
			</button>
		</div>
	)
}

export function Search(props: { value: string; onChange: (v: string) => void; placeholder?: string; autoFocus?: boolean }): ReactNode {
	return (
		<input
			type="search"
			className="search"
			value={props.value}
			placeholder={props.placeholder ?? '검색'}
			onChange={(e) => props.onChange(e.target.value)}
			autoFocus={props.autoFocus}
		/>
	)
}

/** A value that settles a moment after it stops changing, for a search box. */
export function useDebounced<T>(v: T, ms = 250): T {
	const [d, setD] = useState(v)
	useEffect(() => {
		const t = setTimeout(() => setD(v), ms)
		return () => clearTimeout(t)
	}, [v, ms])
	return d
}
