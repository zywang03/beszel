import { Plural, Trans, useLingui } from "@lingui/react/macro"
import { useStore } from "@nanostores/react"
import { getPagePath } from "@nanostores/router"
import {
	type ColumnDef,
	type ColumnFiltersState,
	flexRender,
	getCoreRowModel,
	getFilteredRowModel,
	getSortedRowModel,
	type Row,
	type SortingState,
	type Table as TableType,
	useReactTable,
	type VisibilityState,
} from "@tanstack/react-table"
import { useVirtualizer } from "@tanstack/react-virtual"
import {
	ArrowDownIcon,
	ArrowUpDownIcon,
	ArrowUpIcon,
	EyeIcon,
	FilterIcon,
	LayoutGridIcon,
	LayoutListIcon,
	Settings2Icon,
	XIcon,
} from "lucide-react"
import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react"
import { Button } from "@/components/ui/button"
import { GpuSummaryList } from "@/components/gpu-summary"
import {
	DropdownMenu,
	DropdownMenuCheckboxItem,
	DropdownMenuContent,
	DropdownMenuItem,
	DropdownMenuLabel,
	DropdownMenuRadioGroup,
	DropdownMenuRadioItem,
	DropdownMenuSeparator,
	DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { SystemStatus } from "@/lib/enums"
import { $downSystems, $pausedSystems, $systems, $upSystems } from "@/lib/stores"
import { cn, runOnce, useBrowserStorage } from "@/lib/utils"
import type { SystemRecord } from "@/types"
import AlertButton from "../alerts/alert-button"
import { $router, Link } from "../router"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../ui/card"
import { SystemsTableColumns, ActionsButton, IndicatorDot } from "./systems-table-columns"

type ViewMode = "table" | "grid"
type StatusFilter = "all" | SystemRecord["status"]

const preloadSystemDetail = runOnce(() => import("@/components/routes/system.tsx"))
const DEFAULT_COLUMN_VISIBILITY: VisibilityState = {
	cpu: false,
	loadAverage: false,
	temp: false,
	battery: false,
	services: false,
	uptime: false,
}

function mergeDefaultColumnVisibility(value: VisibilityState): VisibilityState {
	return {
		...DEFAULT_COLUMN_VISIBILITY,
		...value,
	}
}

export default function SystemsTable() {
	const data = useStore($systems)
	const downSystems = $downSystems.get()
	const upSystems = $upSystems.get()
	const pausedSystems = $pausedSystems.get()
	const { i18n, t } = useLingui()
	const [filter, setFilter] = useState<string>("")
	const [statusFilter, setStatusFilter] = useState<StatusFilter>("all")
	const [sorting, setSorting] = useBrowserStorage<SortingState>(
		"sortMode",
		[{ id: "system", desc: false }],
		sessionStorage
	)
	const [columnFilters, setColumnFilters] = useState<ColumnFiltersState>([])
	const [columnVisibility, setColumnVisibility] = useBrowserStorage<VisibilityState>(
		"cols",
		DEFAULT_COLUMN_VISIBILITY
	)

	useEffect(() => {
		setColumnVisibility((prev) => {
			const merged = mergeDefaultColumnVisibility(prev)
			const prevKeys = Object.keys(prev)
			const mergedKeys = Object.keys(merged)
			if (
				prevKeys.length === mergedKeys.length &&
				mergedKeys.every((key) => prev[key] === merged[key])
			) {
				return prev
			}
			return merged
		})
	}, [setColumnVisibility])

	const locale = i18n.locale

	// Filter data based on status filter
	const filteredData = useMemo(() => {
		if (statusFilter === "all") {
			return data
		}
		if (statusFilter === SystemStatus.Up) {
			return Object.values(upSystems) ?? []
		}
		if (statusFilter === SystemStatus.Down) {
			return Object.values(downSystems) ?? []
		}
		return Object.values(pausedSystems) ?? []
	}, [data, statusFilter])

	const [viewMode, setViewMode] = useBrowserStorage<ViewMode>(
		"viewMode",
		"grid"
	)

	useEffect(() => {
		if (filter !== undefined) {
			table.getColumn("system")?.setFilterValue(filter)
		}
	}, [filter])

	const columnDefs = useMemo(() => SystemsTableColumns(viewMode), [viewMode])

	const table = useReactTable({
		data: filteredData,
		columns: columnDefs,
		getCoreRowModel: getCoreRowModel(),
		onSortingChange: setSorting,
		getSortedRowModel: getSortedRowModel(),
		onColumnFiltersChange: setColumnFilters,
		getFilteredRowModel: getFilteredRowModel(),
		onColumnVisibilityChange: setColumnVisibility,
		state: {
			sorting,
			columnFilters,
			columnVisibility,
		},
		defaultColumn: {
			invertSorting: true,
			sortUndefined: "last",
			minSize: 0,
		},
	})

	const rows = table.getRowModel().rows
	const columns = table.getAllColumns()
	const visibleColumns = table.getVisibleLeafColumns()
	const visibleColumnSignature = useMemo(() => {
		return visibleColumns.map((column) => `${column.id}:${column.getIsVisible() ? 1 : 0}`).join("|")
	}, [visibleColumns])
	const gpuRows = useMemo(() => {
		return rows.filter((row) => {
			const summaries = row.original.info.gs
			return summaries && Object.keys(summaries).length > 0
		})
	}, [rows])

	const [upSystemsLength, downSystemsLength, pausedSystemsLength] = useMemo(() => {
		return [Object.values(upSystems).length, Object.values(downSystems).length, Object.values(pausedSystems).length]
	}, [upSystems, downSystems, pausedSystems])

	const CardHead = useMemo(() => {
		return (
			<CardHeader className="pb-4.5 px-2 sm:px-6 max-sm:pt-5 max-sm:pb-1">
				<div className="grid md:flex gap-5 w-full items-end">
					<div className="px-2 sm:px-1">
						<CardTitle className="mb-2">
							<Trans>All Systems</Trans>
						</CardTitle>
						<CardDescription className="flex">
							<Trans>Click on a system to view more information.</Trans>
						</CardDescription>
					</div>

					<div className="flex gap-2 ms-auto w-full md:w-80">
						<div className="relative flex-1">
							<Input
								placeholder={t`Filter...`}
								onChange={(e) => setFilter(e.target.value)}
								value={filter}
								className="ps-4 pe-10 w-full"
							/>
							{filter && (
								<Button
									type="button"
									variant="ghost"
									size="icon"
									aria-label={t`Clear`}
									className="absolute right-1 top-1/2 -translate-y-1/2 h-7 w-7 text-muted-foreground"
									onClick={() => setFilter("")}
								>
									<XIcon className="h-4 w-4" />
								</Button>
							)}
						</div>
						<DropdownMenu>
							<DropdownMenuTrigger asChild>
								<Button variant="outline">
									<Settings2Icon className="me-1.5 size-4 opacity-80" />
									<Trans>View</Trans>
								</Button>
							</DropdownMenuTrigger>
							<DropdownMenuContent align="end" className="h-72 md:h-auto min-w-48 md:min-w-auto overflow-y-auto">
								<div className="grid grid-cols-1 md:grid-cols-4 divide-y md:divide-s md:divide-y-0">
									<div className="border-r">
										<DropdownMenuLabel className="pt-2 px-3.5 flex items-center gap-2">
											<LayoutGridIcon className="size-4" />
											<Trans>Layout</Trans>
										</DropdownMenuLabel>
										<DropdownMenuSeparator />
										<DropdownMenuRadioGroup
											className="px-1 pb-1"
											value={viewMode}
											onValueChange={(view) => setViewMode(view as ViewMode)}
										>
											<DropdownMenuRadioItem value="table" onSelect={(e) => e.preventDefault()} className="gap-2">
												<LayoutListIcon className="size-4" />
												<Trans>Table</Trans>
											</DropdownMenuRadioItem>
											<DropdownMenuRadioItem value="grid" onSelect={(e) => e.preventDefault()} className="gap-2">
												<LayoutGridIcon className="size-4" />
												<Trans>Grid</Trans>
											</DropdownMenuRadioItem>
										</DropdownMenuRadioGroup>
									</div>

									<div className="border-r">
										<DropdownMenuLabel className="pt-2 px-3.5 flex items-center gap-2">
											<FilterIcon className="size-4" />
											<Trans>Status</Trans>
										</DropdownMenuLabel>
										<DropdownMenuSeparator />
										<DropdownMenuRadioGroup
											className="px-1 pb-1"
											value={statusFilter}
											onValueChange={(value) => setStatusFilter(value as StatusFilter)}
										>
											<DropdownMenuRadioItem value="all" onSelect={(e) => e.preventDefault()}>
												<Trans>All Systems</Trans>
											</DropdownMenuRadioItem>
											<DropdownMenuRadioItem value="up" onSelect={(e) => e.preventDefault()}>
												<Trans>Up ({upSystemsLength})</Trans>
											</DropdownMenuRadioItem>
											<DropdownMenuRadioItem value="down" onSelect={(e) => e.preventDefault()}>
												<Trans>Down ({downSystemsLength})</Trans>
											</DropdownMenuRadioItem>
											<DropdownMenuRadioItem value="paused" onSelect={(e) => e.preventDefault()}>
												<Trans>Paused ({pausedSystemsLength})</Trans>
											</DropdownMenuRadioItem>
										</DropdownMenuRadioGroup>
									</div>

									<div className="border-r">
										<DropdownMenuLabel className="pt-2 px-3.5 flex items-center gap-2">
											<ArrowUpDownIcon className="size-4" />
											<Trans>Sort By</Trans>
										</DropdownMenuLabel>
										<DropdownMenuSeparator />
										<div className="px-1 pb-1">
											{columns.map((column) => {
												if (!column.getCanSort()) return null
												let Icon = <span className="w-6"></span>
												// if current sort column, show sort direction
												if (sorting[0]?.id === column.id) {
													if (sorting[0]?.desc) {
														Icon = <ArrowUpIcon className="me-2 size-4" />
													} else {
														Icon = <ArrowDownIcon className="me-2 size-4" />
													}
												}
												return (
													<DropdownMenuItem
														onSelect={(e) => {
															e.preventDefault()
															setSorting([{ id: column.id, desc: sorting[0]?.id === column.id && !sorting[0]?.desc }])
														}}
														key={column.id}
													>
														{Icon}
														{/* @ts-ignore */}
														{column.columnDef.name()}
													</DropdownMenuItem>
												)
											})}
										</div>
									</div>

									<div>
										<DropdownMenuLabel className="pt-2 px-3.5 flex items-center gap-2">
											<EyeIcon className="size-4" />
											<Trans>Visible Fields</Trans>
										</DropdownMenuLabel>
										<DropdownMenuSeparator />
										<div className="px-1.5 pb-1">
											{columns
												.filter((column) => column.getCanHide())
												.map((column) => {
													return (
														<DropdownMenuCheckboxItem
															key={column.id}
															onSelect={(e) => e.preventDefault()}
															checked={column.getIsVisible()}
															onCheckedChange={(value) => column.toggleVisibility(!!value)}
														>
															{/* @ts-ignore */}
															{column.columnDef.name()}
														</DropdownMenuCheckboxItem>
													)
												})}
										</div>
									</div>
								</div>
							</DropdownMenuContent>
						</DropdownMenu>
					</div>
				</div>
			</CardHeader>
		)
	}, [
		visibleColumnSignature,
		sorting,
		viewMode,
		locale,
		statusFilter,
		upSystemsLength,
		downSystemsLength,
		pausedSystemsLength,
		filter,
	])

	return (
		<div className="grid w-full min-w-0 max-w-full gap-4">
			<Card className="w-full min-w-0 max-w-full">
				{CardHead}
				<div className="p-6 pt-0 max-sm:py-3 max-sm:px-2">
					{viewMode === "table" ? (
						<div className="rounded-md">
							<AllSystemsTable
								table={table}
								rows={rows}
								colLength={visibleColumns.length}
								visibleColumnSignature={visibleColumnSignature}
							/>
						</div>
					) : (
						<div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-3">
							{rows?.length ? (
								rows.map((row) => {
									return (
										<SystemCard
											key={row.original.id}
											row={row}
											table={table}
											colLength={visibleColumns.length}
											visibleColumnSignature={visibleColumnSignature}
										/>
									)
								})
							) : (
								<div className="col-span-full text-center py-8">
									<Trans>No systems found.</Trans>
								</div>
							)}
						</div>
					)}
				</div>
			</Card>
			{viewMode === "table" && gpuRows.length > 0 && <SystemsGpuPanel rows={gpuRows} />}
		</div>
	)
}

const AllSystemsTable = memo(
	({
		table,
		rows,
		colLength,
		visibleColumnSignature,
	}: {
		table: TableType<SystemRecord>
		rows: Row<SystemRecord>[]
		colLength: number
		visibleColumnSignature: string
	}) => {
		// The virtualizer will need a reference to the scrollable container element
		const scrollRef = useRef<HTMLDivElement>(null)

		const virtualizer = useVirtualizer<HTMLDivElement, HTMLTableRowElement>({
			count: rows.length,
			estimateSize: () => 60,
			getItemKey: (index) => rows[index]?.id ?? index,
			getScrollElement: () => scrollRef.current,
			overscan: 5,
			measureElement: (element) => element?.getBoundingClientRect().height ?? 60,
		})
		const virtualRows = virtualizer.getVirtualItems()

		const paddingTop = Math.max(0, virtualRows[0]?.start ?? 0 - virtualizer.options.scrollMargin)
		const paddingBottom = Math.max(0, virtualizer.getTotalSize() - (virtualRows[virtualRows.length - 1]?.end ?? 0))

		return (
			<div
				className={cn(
					"h-min max-h-[calc(100dvh-17rem)] max-w-full relative overflow-auto border rounded-md",
					// don't set min height if there are less than 2 rows, do set if we need to display the empty state
					(!rows.length || rows.length > 2) && "min-h-50"
				)}
				ref={scrollRef}
			>
				<table className="w-full table-auto text-sm">
					<SystemsTableHead table={table} />
					<TableBody onMouseEnter={preloadSystemDetail}>
						{rows.length ? (
							<>
								{paddingTop > 0 && (
									<TableRow className="border-0 hover:bg-transparent">
										<TableCell colSpan={colLength} className="h-0 p-0" style={{ height: paddingTop }} />
									</TableRow>
								)}
								{virtualRows.map((virtualRow) => {
									const row = rows[virtualRow.index] as Row<SystemRecord>
									return (
										<SystemTableRow
											key={row.id}
											row={row}
											measureElement={virtualizer.measureElement}
											visibleColumnSignature={visibleColumnSignature}
										/>
									)
								})}
								{paddingBottom > 0 && (
									<TableRow className="border-0 hover:bg-transparent">
										<TableCell colSpan={colLength} className="h-0 p-0" style={{ height: paddingBottom }} />
									</TableRow>
								)}
							</>
						) : (
							<TableRow>
								<TableCell colSpan={colLength} className="h-37 text-center pointer-events-none">
									<Trans>No systems found.</Trans>
								</TableCell>
							</TableRow>
						)}
					</TableBody>
				</table>
			</div>
		)
	}
)

function SystemsTableHead({ table }: { table: TableType<SystemRecord> }) {
	return (
		<TableHeader className="sticky top-0 z-50 w-full border-b-2">
			<div className="absolute -top-2 left-0 w-full h-4 bg-table-header z-50"></div>
			{table.getHeaderGroups().map((headerGroup) => (
				<tr key={headerGroup.id}>
					{headerGroup.headers.map((header) => {
						return (
							<TableHead className="px-1.5" key={header.id}>
								{flexRender(header.column.columnDef.header, header.getContext())}
							</TableHead>
						)
					})}
				</tr>
			))}
		</TableHeader>
	)
}

const SystemTableRow = memo(
	({
		row,
		measureElement,
		visibleColumnSignature,
	}: {
		row: Row<SystemRecord>
		measureElement: (node: HTMLTableRowElement | null) => void
		visibleColumnSignature: string
	}) => {
		const system = row.original
		const setMainRowRef = useCallback(
			(node: HTMLTableRowElement | null) => {
				measureElement(node)
			},
			[measureElement]
		)

		return (
			<TableRow
				ref={setMainRowRef}
				// data-state={row.getIsSelected() && "selected"}
				className={cn("cursor-pointer transition-opacity relative safari:transform-3d", {
					"opacity-50": system.status === SystemStatus.Paused,
				})}
			>
				{row.getVisibleCells().map((cell) => (
					<TableCell key={cell.id} className="py-3 ps-4.5 align-middle">
						{flexRender(cell.column.columnDef.cell, cell.getContext())}
					</TableCell>
				))}
			</TableRow>
		)
	}
)

const SystemsGpuPanel = memo(({ rows }: { rows: Row<SystemRecord>[] }) => {
	return (
		<Card className="w-full min-w-0 max-w-full overflow-hidden">
			<CardHeader className="pb-4.5 px-2 sm:px-6 max-sm:pt-5 max-sm:pb-1">
				<div className="px-2 sm:px-1">
					<CardTitle className="mb-2">
						<Trans>GPU Overview</Trans>
					</CardTitle>
					<CardDescription className="flex">
						<Trans>Detailed GPU usage for systems with GPU data.</Trans>
					</CardDescription>
				</div>
			</CardHeader>
			<CardContent className="grid gap-4 px-2 pb-3 sm:px-6 sm:pb-6">
				<div className="grid min-w-0 gap-4 grid-cols-1">
					{rows.map((row) => {
						const system = row.original
						const summaries = system.info.gs
						if (!summaries || Object.keys(summaries).length === 0) {
							return null
						}

						return (
							<div
								key={system.id}
								className={cn("w-full min-w-0 max-w-full overflow-hidden rounded-md border border-border/70 bg-muted/10 p-4", {
									"opacity-50": system.status === SystemStatus.Paused,
								})}
							>
								<div className="mb-3 flex items-center justify-between gap-3">
									<Link
										href={getPagePath($router, "system", { id: system.id })}
										className="relative z-10 flex min-w-0 items-center gap-2 font-medium"
									>
										<IndicatorDot system={system} />
										<span className="truncate">{system.name}</span>
									</Link>
									<div className="shrink-0 text-xs tabular-nums text-muted-foreground">
										<Plural value={Object.keys(summaries).length} one="# GPU" other="# GPUs" />
									</div>
								</div>
								<GpuSummaryList gpus={summaries} status={system.status} className="min-w-0" />
							</div>
						)
					})}
				</div>
			</CardContent>
		</Card>
	)
})

const SystemCard = memo(
	({
		row,
		table,
		colLength,
		visibleColumnSignature,
	}: {
		row: Row<SystemRecord>
		table: TableType<SystemRecord>
		colLength: number
		visibleColumnSignature: string
	}) => {
		const system = row.original
		const { t } = useLingui()

		return useMemo(() => {
			const gpuColumn = table.getColumn("gpu")
			const showGpu = gpuColumn?.getIsVisible() ?? false
			const gpuCell = row.getAllCells().find((cell) => cell.column.id === "gpu")

			return (
				<Card
					onMouseEnter={preloadSystemDetail}
					key={system.id}
					className={cn(
						"cursor-pointer hover:shadow-md transition-all bg-transparent w-full min-w-0 overflow-hidden dark:border-border duration-200 relative",
						{
							"opacity-50": system.status === SystemStatus.Paused,
						}
					)}
				>
					<CardHeader className="py-2 ps-5 pe-3 bg-muted/30 border-b border-border/60">
						<div className="flex items-start gap-2 w-full overflow-hidden">
							<div className="min-w-0 flex-1">
								<CardTitle className="text-base tracking-normal text-primary/90 flex items-center min-w-0 gap-2.5">
									<div className="flex items-center gap-2.5 min-w-0 flex-1">
										<IndicatorDot system={system} />
										<span className="text-[.95em]/normal tracking-normal text-primary/90 truncate">{system.name}</span>
									</div>
								</CardTitle>
								{(system.device_admin || system.location) && (
									<div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 ps-4.5 text-xs text-muted-foreground">
										{system.device_admin && (
											<span className="truncate">
												<Trans>管理员</Trans>：{system.device_admin}
											</span>
										)}
										{system.location && (
											<span className="truncate">
												<Trans>位置</Trans>：{system.location}
											</span>
										)}
									</div>
								)}
							</div>
							{table.getColumn("actions")?.getIsVisible() && (
								<div className="flex gap-1 shrink-0 relative z-10">
									<AlertButton system={system} />
									<ActionsButton system={system} />
								</div>
							)}
						</div>
					</CardHeader>
					<CardContent className="text-sm px-5 pt-3.5 pb-4">
						<div className="grid gap-2.5" style={{ gridTemplateColumns: "24px minmax(80px, max-content) 1fr" }}>
							{table.getAllColumns().map((column) => {
								if (!column.getIsVisible() || column.id === "system" || column.id === "actions" || column.id === "gpu") {
									return null
								}
								const cell = row.getAllCells().find((cell) => cell.column.id === column.id)
								if (!cell) return null
								// @ts-expect-error
								const { Icon, name } = column.columnDef as ColumnDef<SystemRecord, unknown>
								return (
									<>
										<div key={`${column.id}-icon`} className="flex items-center">
											{column.id === "lastSeen" ? (
												<EyeIcon className="size-4 text-muted-foreground" />
											) : (
												Icon && <Icon className="size-4 text-muted-foreground" />
											)}
										</div>
										<div key={`${column.id}-label`} className="flex items-center text-muted-foreground pr-3">
											{name()}:
										</div>
										<div key={`${column.id}-value`} className="flex items-center">
											{flexRender(cell.column.columnDef.cell, cell.getContext())}
										</div>
									</>
								)
							})}
						</div>
						{showGpu && gpuCell && (
							<div className="relative z-10 mt-4 min-w-0 overflow-hidden border-t border-border/60 pt-4 cursor-auto">
								<div className="flex items-center gap-2 text-muted-foreground mb-2">
									{/* @ts-expect-error */}
									{gpuColumn.columnDef.Icon && <gpuColumn.columnDef.Icon className="size-4" />}
									<span className="text-sm font-medium">
										<Trans>GPU</Trans>
									</span>
								</div>
								<div className="w-full min-w-0 overflow-hidden">
									{flexRender(gpuCell.column.columnDef.cell, gpuCell.getContext())}
								</div>
							</div>
						)}
					</CardContent>
					<Link
						href={getPagePath($router, "system", { id: row.original.id })}
						className="inset-0 absolute w-full h-full"
					>
						<span className="sr-only">{row.original.name}</span>
					</Link>
				</Card>
			)
		}, [system, colLength, t, visibleColumnSignature])
	}
)
