import { Plural, Trans, useLingui } from "@lingui/react/macro"
import { useStore } from "@nanostores/react"
import type { GPUConsumer, GPUData } from "@/types"
import { MeterState, SystemStatus } from "@/lib/enums"
import { $userSettings } from "@/lib/stores"
import { cn, decimalString, formatBytes, secondsToString } from "@/lib/utils"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Separator } from "@/components/ui/separator"

const gpuListGridStyle = {
	gridTemplateColumns: "2.2rem minmax(6.25rem, 1.35fr) minmax(4.25rem, 0.95fr) minmax(0, 3.9fr)",
}

function formatGpuMemory(memory = 0) {
	const { value, unit } = formatBytes(memory, false, undefined, true)
	return `${decimalString(value, value >= 10 ? 1 : 2)} ${unit}`
}

function formatConsumerRuntime(seconds: number | undefined, lessThanMinuteLabel: string) {
	if (!seconds || seconds < 60) {
		return lessThanMinuteLabel
	}
	if (seconds < 3600) {
		return secondsToString(seconds, "minute")
	}
	if (seconds < 360000) {
		return secondsToString(seconds, "hour")
	}
	return secondsToString(seconds, "day")
}

function isUnattributedConsumer(consumer: GPUConsumer) {
	return consumer.i?.trim().toLowerCase() === "unattributed" || consumer.n?.trim().toLowerCase() === "unattributed"
}

function sortConsumersForDisplay(consumers: GPUConsumer[]) {
	return [...consumers].sort((a, b) => Number(isUnattributedConsumer(a)) - Number(isUnattributedConsumer(b)))
}

function getConsumerBadgeClass(consumer: GPUConsumer, compact: boolean) {
	return cn(
		"font-normal",
		compact ? "h-5 rounded-sm px-1.5 py-0 text-[11px] leading-none" : "rounded-md px-2 py-1",
		isUnattributedConsumer(consumer) && "border-border/80 bg-muted/80 text-muted-foreground"
	)
}

function sortGpus(gpus: Record<string, GPUData>) {
	return Object.entries(gpus).sort(([a], [b]) => a.localeCompare(b, undefined, { numeric: true }))
}

function getGpuGroupName(name: string | undefined, id: string) {
	if (!name) {
		return "GPU"
	}

	const trimmed = name.trim()
	const suffix = ` ${id}`
	if (trimmed.endsWith(suffix)) {
		return trimmed.slice(0, -suffix.length).trim() || trimmed
	}

	return trimmed
}

function summarizeGpuModels(gpus: Record<string, GPUData>) {
	const counts = new Map<string, number>()

	for (const [id, gpu] of sortGpus(gpus)) {
		const key = getGpuGroupName(gpu.n, id)
		counts.set(key, (counts.get(key) ?? 0) + 1)
	}

	return Array.from(counts.entries()).map(([name, count]) => ({ name, count }))
}

function getMeterStateByThresholds(value: number, warn = 65, crit = 90): MeterState {
	return value >= crit ? MeterState.Crit : value >= warn ? MeterState.Warn : MeterState.Good
}

function getMeterClass(percent: number, warn: number, crit: number, status?: SystemStatus) {
	if (status && status !== SystemStatus.Up) {
		return "bg-primary/40"
	}

	const threshold = getMeterStateByThresholds(percent, warn, crit)
	if (threshold === MeterState.Crit) {
		return "bg-red-500"
	}
	if (threshold === MeterState.Warn) {
		return "bg-yellow-500"
	}
	return "bg-green-500"
}

function MetricBar({
	label,
	value,
	percent,
	status,
}: {
	label: string
	value: string
	percent: number
	status?: SystemStatus
}) {
	const { colorWarn = 65, colorCrit = 90 } = useStore($userSettings, { keys: ["colorWarn", "colorCrit"] })
	const width = Math.max(0, Math.min(100, percent))
	const meterClass = getMeterClass(percent, colorWarn, colorCrit, status)

	return (
		<div className="grid gap-1">
			<div className="flex items-center justify-between gap-3 text-xs">
				<span className="text-muted-foreground">{label}</span>
				<span className="tabular-nums">{value}</span>
			</div>
			<div className="h-1.5 overflow-hidden rounded-full bg-muted">
				<div className={cn("h-full rounded-full transition-all", meterClass)} style={{ width: `${width}%` }} />
			</div>
		</div>
	)
}

function Consumers({
	consumers,
	showEmptyState,
	compact = false,
	singleLine = false,
}: {
	consumers?: GPUConsumer[]
	showEmptyState?: boolean
	compact?: boolean
	singleLine?: boolean
}) {
	const { t } = useLingui()
	const displayConsumers = consumers ? sortConsumersForDisplay(consumers) : []

	if (!displayConsumers.length) {
		if (!showEmptyState) {
			return null
		}
		return (
			<div className={cn("text-xs text-muted-foreground", singleLine && "truncate whitespace-nowrap pe-2")}>
				<Trans>No containers using this GPU</Trans>
			</div>
		)
	}

	if (singleLine) {
		return (
			<div className="min-w-0 max-w-full overflow-hidden">
				<div className="w-full max-w-full overflow-x-auto pb-1">
					<div className={cn("inline-flex min-w-max flex-nowrap gap-1 pe-2", compact ? "gap-1" : "gap-1.5")}>
						{displayConsumers.map((consumer) => (
							<Badge
								key={`${consumer.i}-${consumer.n}`}
								variant="outline"
								className={cn("shrink-0 whitespace-nowrap", getConsumerBadgeClass(consumer, compact))}
							>
								<span className={cn("truncate", compact ? "max-w-28" : "max-w-40")}>{consumer.n || consumer.i}</span>
								{consumer.mu !== undefined && (
									<span className="ms-1.5 tabular-nums text-muted-foreground">{formatGpuMemory(consumer.mu)}</span>
								)}
								{consumer.rt !== undefined && (
									<span className="ms-1.5 tabular-nums text-muted-foreground">
										{formatConsumerRuntime(consumer.rt, t`<1 minute`)}
									</span>
								)}
							</Badge>
						))}
					</div>
				</div>
			</div>
		)
	}

	return (
		<div className={cn("flex flex-wrap gap-1.5", compact && "gap-1")}>
			{displayConsumers.map((consumer) => (
				<Badge
					key={`${consumer.i}-${consumer.n}`}
					variant="outline"
					className={getConsumerBadgeClass(consumer, compact)}
				>
					<span className={cn("truncate", compact ? "max-w-28" : "max-w-40")}>{consumer.n || consumer.i}</span>
					{consumer.mu !== undefined && (
						<span className="ms-1.5 tabular-nums text-muted-foreground">{formatGpuMemory(consumer.mu)}</span>
					)}
					{consumer.rt !== undefined && (
						<span className="ms-1.5 tabular-nums text-muted-foreground">
							{formatConsumerRuntime(consumer.rt, t`<1 minute`)}
						</span>
					)}
				</Badge>
			))}
		</div>
	)
}

function CompactMetric({
	value,
	percent,
	status,
	align = "right",
}: {
	value: string
	percent: number
	status?: SystemStatus
	align?: "left" | "right"
}) {
	const { colorWarn = 65, colorCrit = 90 } = useStore($userSettings, { keys: ["colorWarn", "colorCrit"] })
	const width = Math.max(0, Math.min(100, percent))
	const meterClass = getMeterClass(percent, colorWarn, colorCrit, status)

	return (
		<div className="grid min-w-0 gap-1">
			<div
				className={cn(
					"truncate text-[11px] leading-none tabular-nums whitespace-nowrap",
					align === "left" ? "text-left" : "text-right"
				)}
			>
				{value}
			</div>
			<div className="h-1.5 overflow-hidden rounded-full bg-muted">
				<div className={cn("h-full rounded-full transition-all", meterClass)} style={{ width: `${width}%` }} />
			</div>
		</div>
	)
}

export function GpuSummaryList({
	gpus,
	className,
	showEmptyConsumers = false,
	status,
}: {
	gpus?: Record<string, GPUData>
	className?: string
	showEmptyConsumers?: boolean
	status?: SystemStatus
}) {
	const { t } = useLingui()

	if (!gpus || Object.keys(gpus).length === 0) {
		return null
	}

	const modelSummaries = summarizeGpuModels(gpus)
	const totalGpuCount = sortGpus(gpus).length

	return (
		<div className={cn("grid w-full min-w-0 max-w-full gap-2.5", className)}>
			<div className="w-full min-w-0 max-w-full overflow-hidden rounded-md border border-border/70 bg-muted/15">
				<div className="flex items-center justify-between gap-3 border-b border-border/60 bg-muted/35 px-3 py-2">
					<div className="min-w-0 flex flex-wrap items-center gap-2">
						{modelSummaries.map(({ name, count }) => (
							<Badge key={name} variant="secondary" className="max-w-full gap-1 rounded-md px-2 py-1 font-medium">
								<span className="truncate">{name}</span>
								<span className="tabular-nums text-muted-foreground">x{count}</span>
							</Badge>
						))}
					</div>
					<div className="shrink-0 text-[11px] tabular-nums text-muted-foreground">
						<Plural value={totalGpuCount} one="# GPU" other="# GPUs" />
					</div>
				</div>
				<div
					className="grid w-full min-w-0 max-w-full items-center gap-3 border-b border-border/60 bg-background/75 px-3 py-1.5 text-[10px] font-medium uppercase tracking-[0.14em] text-muted-foreground"
					style={gpuListGridStyle}
				>
					<div>
						<Trans>GPU</Trans>
					</div>
					<div>
						<Trans>VRAM</Trans>
					</div>
					<div>
						<Trans>Util</Trans>
					</div>
					<div>
						<Trans>Containers</Trans>
					</div>
				</div>

				<div className="divide-y divide-border/60">
					{sortGpus(gpus).map(([id, gpu]) => {
						const memPct = (gpu.mt ?? 0) > 0 ? ((gpu.mu ?? 0) / (gpu.mt ?? 1)) * 100 : 0
						const vramValue = (gpu.mt ?? 0) > 0 ? `${formatGpuMemory(gpu.mu)} / ${formatGpuMemory(gpu.mt)}` : "-"

						return (
							<div key={id} className="bg-background/90 px-3 py-2.5">
								<div className="grid w-full min-w-0 max-w-full items-center gap-3" style={gpuListGridStyle}>
									<div
										className="pt-0.5 text-xs font-medium tabular-nums text-muted-foreground"
										title={gpu.n || t({ message: "GPU {id}", values: { id } })}
									>
										{id}
									</div>
									<CompactMetric value={vramValue} percent={memPct} status={status} align="left" />
									<CompactMetric
										value={`${decimalString(gpu.u ?? 0, (gpu.u ?? 0) >= 10 ? 1 : 2)}%`}
										percent={gpu.u ?? 0}
										status={status}
										align="left"
									/>
									<div className="min-w-0 max-w-full">
										<Consumers consumers={gpu.c} showEmptyState={showEmptyConsumers} compact singleLine />
									</div>
								</div>
							</div>
						)
					})}
				</div>
			</div>
		</div>
	)
}

export function GpuSummaryCards({ gpus, status }: { gpus?: Record<string, GPUData>; status?: SystemStatus }) {
	const { t } = useLingui()

	if (!gpus || Object.keys(gpus).length === 0) {
		return null
	}

	return sortGpus(gpus).map(([id, gpu]) => {
		const memPct = (gpu.mt ?? 0) > 0 ? ((gpu.mu ?? 0) / (gpu.mt ?? 1)) * 100 : 0
		return (
			<Card key={id} className="min-h-full">
				<CardHeader className="pb-4">
					<div className="flex items-center justify-between gap-3">
						<div className="min-w-0">
							<CardTitle className="truncate text-xl">{gpu.n}</CardTitle>
							<CardDescription>
								<Trans>GPU {id}</Trans>
							</CardDescription>
						</div>
						<Badge variant="outline" className="tabular-nums">
							{decimalString(gpu.u ?? 0, (gpu.u ?? 0) >= 10 ? 1 : 2)}%
						</Badge>
					</div>
				</CardHeader>
				<CardContent className="grid gap-4">
					<MetricBar
						label={t`Utilization`}
						value={`${decimalString(gpu.u ?? 0, (gpu.u ?? 0) >= 10 ? 1 : 2)}%`}
						percent={gpu.u ?? 0}
						status={status}
					/>
					{(gpu.mt ?? 0) > 0 && (
						<MetricBar
							label={t`VRAM`}
							value={`${formatGpuMemory(gpu.mu)} / ${formatGpuMemory(gpu.mt)}`}
							percent={memPct}
							status={status}
						/>
					)}
					<Separator />
					<div className="grid gap-2">
						<div className="text-sm font-medium">
							<Trans>Containers</Trans>
						</div>
						<Consumers consumers={gpu.c} showEmptyState={true} />
					</div>
				</CardContent>
			</Card>
		)
	})
}
