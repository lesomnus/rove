/**
 * Reads and writes, the plain way.
 *
 * A read is a call made when a component mounts and made again when its
 * request changes or **anything was written** -- a write bumps one counter
 * every mounted read listens to, so whatever is on screen is read again and
 * nothing else is. A screen that should follow other people's changes asks to
 * be polled.
 *
 * payday's store would do better than this -- one copy of a row however many
 * places draw it, and a `Watch` instead of a poll -- and is what this should
 * become. It is not used yet because most of what rove reads is not a row:
 * a timeline, a tree at a moment, a calendar, a report.
 *
 * @module
 */

import type { DescMessage, DescMethodUnary, DescService, MessageInitShape, MessageShape } from '@bufbuild/protobuf'
import { createClient } from '@connectrpc/connect'
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'

import { errorText, isUnauthenticated, transport } from './api.js'
import { useToast } from './ui.js'

// eslint-disable-next-line @typescript-eslint/no-explicit-any
const clients = new Map<DescService, any>()

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function clientOf(s: DescService): any {
	let c = clients.get(s)
	if (c === undefined) {
		c = createClient(s, transport)
		clients.set(s, c)
	}
	return c
}

/** call makes one unary call. */
export async function call<I extends DescMessage, O extends DescMessage>(
	method: DescMethodUnary<I, O>,
	input: MessageInitShape<I>,
	signal?: AbortSignal,
): Promise<MessageShape<O>> {
	return clientOf(method.parent)[method.localName](input, signal === undefined ? undefined : { signal })
}

type Epoch = { epoch: number; bump: () => void; onSignedOut: () => void }
const EpochCtx = createContext<Epoch>({ epoch: 0, bump: () => {}, onSignedOut: () => {} })

export function Rpc(props: { children: ReactNode; onSignedOut: () => void }): ReactNode {
	const [epoch, setEpoch] = useState(0)
	const bump = useCallback(() => setEpoch((v) => v + 1), [])
	const v = useMemo(() => ({ epoch, bump, onSignedOut: props.onSignedOut }), [epoch, bump, props.onSignedOut])
	return <EpochCtx.Provider value={v}>{props.children}</EpochCtx.Provider>
}

export function useEpoch(): Epoch {
	return useContext(EpochCtx)
}

/** A request as a string, so that the same question is the same key. */
function keyOf(v: unknown): string {
	return JSON.stringify(v, (_, x) => {
		if (typeof x === 'bigint') return `${x}n`
		if (x instanceof Uint8Array) return `b:${Array.from(x, (b) => b.toString(16).padStart(2, '0')).join('')}`
		return x
	})
}

export interface Read<T> {
	state: 'pending' | 'ok' | 'error'
	data: T | undefined
	error: unknown
	reload: () => void
}

/**
 * useRpc reads one call, again whenever its request changes or anything was
 * written. `null` for a request is not asking yet.
 *
 * What it answered last stays drawn while it is asked again, so a page does
 * not blink to a spinner every time somebody saves something.
 */
export function useRpc<I extends DescMessage, O extends DescMessage>(
	method: DescMethodUnary<I, O>,
	input: MessageInitShape<I> | null,
	opts: { poll?: number } = {},
): Read<MessageShape<O>> {
	const { epoch, onSignedOut } = useEpoch()
	const key = input === null ? null : `${method.parent.typeName}/${method.name}:${keyOf(input)}`
	const [nonce, setNonce] = useState(0)
	const [at, setAt] = useState<{ key: string | null; state: Read<unknown>['state']; data: MessageShape<O> | undefined; error: unknown }>({
		key: null,
		state: 'pending',
		data: undefined,
		error: undefined,
	})
	const inputRef = useRef(input)
	inputRef.current = input

	useEffect(() => {
		if (key === null || inputRef.current === null) return
		const ctl = new AbortController()
		call(method, inputRef.current, ctl.signal).then(
			(data) => {
				if (!ctl.signal.aborted) setAt({ key, state: 'ok', data, error: undefined })
			},
			(error) => {
				if (ctl.signal.aborted) return
				if (isUnauthenticated(error)) onSignedOut()
				setAt((v) => ({ key, state: 'error', data: v.key === key ? v.data : undefined, error }))
			},
		)
		return () => ctl.abort()
	}, [key, epoch, nonce, method, onSignedOut])

	useEffect(() => {
		if (opts.poll === undefined || key === null) return
		const t = setInterval(() => setNonce((v) => v + 1), opts.poll)
		return () => clearInterval(t)
	}, [opts.poll, key])

	const reload = useCallback(() => setNonce((v) => v + 1), [])

	// What was read for another request is not this one's answer; while the
	// new one is on its way the old one is still drawn, as pending.
	const same = at.key === key
	return {
		state: same ? at.state : 'pending',
		data: at.data,
		error: same ? at.error : undefined,
		reload,
	}
}

export type Act = <I extends DescMessage, O extends DescMessage>(
	method: DescMethodUnary<I, O>,
	input: MessageInitShape<I>,
	opts?: { ok?: string; quiet?: boolean },
) => Promise<MessageShape<O> | undefined>

/**
 * useAct is a write: it says what happened, and has every read on screen
 * asked again. It answers undefined when the write failed, having said why.
 */
export function useAct(): Act {
	const toast = useToast()
	const { bump, onSignedOut } = useEpoch()
	return useCallback(
		async (method, input, opts = {}) => {
			try {
				const v = await call(method, input)
				if (opts.ok !== undefined) toast(opts.ok)
				bump()
				return v
			} catch (err) {
				if (isUnauthenticated(err)) onSignedOut()
				if (!opts.quiet) toast(errorText(err), 'bad')
				return undefined
			}
		},
		[toast, bump, onSignedOut],
	) as Act
}
