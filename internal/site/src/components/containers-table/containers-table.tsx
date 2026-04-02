/** biome-ignore-all lint/security/noDangerouslySetInnerHtml: html comes directly from docker via agent */
import { useStore } from "@nanostores/react"
import { t } from "@lingui/core/macro"
import { Trans } from "@lingui/react/macro"
import {
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
import { useVirtualizer, type VirtualItem } from "@tanstack/react-virtual"
import { memo, type RefObject, useEffect, useRef, useState } from "react"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Textarea } from "@/components/ui/textarea"
import { toast } from "@/components/ui/use-toast"
import { isAdmin, pb } from "@/lib/api"
import {
	buildDockerTransferTaskPreview,
	type DockerTransferConfig,
	type DockerTransferMountBinding,
	type DockerTransferTaskPayload,
	type DockerTransferTaskResponse,
	type DockerTransferTaskStatusResponse,
	dockerTransferRelayDefaults,
	getDefaultNodeHost,
	getDefaultNodeUser,
	getDefaultTargetImage,
} from "@/lib/docker-transfer"
import type { ContainerRecord } from "@/types"
import { containerChartCols } from "@/components/containers-table/containers-table-columns"
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { type ContainerHealth, ContainerHealthLabels } from "@/lib/enums"
import { cn, copyToClipboard, useBrowserStorage } from "@/lib/utils"
import { Sheet, SheetTitle, SheetHeader, SheetContent, SheetDescription } from "../ui/sheet"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "../ui/dialog"
import { Button } from "@/components/ui/button"
import { $allSystemsById, $systems } from "@/lib/stores"
import { AlertCircleIcon, CheckCircle2Icon, Clock3Icon, LoaderCircleIcon, MaximizeIcon, RefreshCwIcon, XIcon } from "lucide-react"
import { Separator } from "../ui/separator"
import { $router, Link } from "../router"
import { listenKeys } from "nanostores"
import { getPagePath } from "@nanostores/router"

const syntaxTheme = "github-dark-dimmed"

export default function ContainersTable({ systemId }: { systemId?: string }) {
	const loadTime = Date.now()
	const [data, setData] = useState<ContainerRecord[] | undefined>(undefined)
	const [sorting, setSorting] = useBrowserStorage<SortingState>(
		`sort-c-${systemId ? 1 : 0}`,
		[{ id: systemId ? "name" : "system", desc: false }],
		sessionStorage
	)
	const [columnFilters, setColumnFilters] = useState<ColumnFiltersState>([])
	const [columnVisibility, setColumnVisibility] = useState<VisibilityState>({})

	// Hide ports column if no ports are present
	useEffect(() => {
		if (data) {
			const hasPorts = data.some((container) => container.ports)
			setColumnVisibility((prev) => {
				if (prev.ports === hasPorts) {
					return prev
				}
				return { ...prev, ports: hasPorts }
			})
		}
	}, [data])

	const [rowSelection, setRowSelection] = useState({})
	const [globalFilter, setGlobalFilter] = useState("")

	useEffect(() => {
		function fetchData(systemId?: string) {
			pb.collection<ContainerRecord>("containers")
				.getList(0, 2000, {
					fields: "id,name,image,ports,cpu,memory,net,health,status,system,updated",
					filter: systemId ? pb.filter("system={:system}", { system: systemId }) : undefined,
				})
				.then(({ items }) => {
					if (items.length === 0) {
						setData((curItems) => {
							if (systemId) {
								return curItems?.filter((item) => item.system !== systemId) ?? []
							}
							return []
						})
						return
					}
					setData((curItems) => {
						const lastUpdated = Math.max(items[0].updated, items.at(-1)?.updated ?? 0)
						const containerIds = new Set()
						const newItems: ContainerRecord[] = []
						for (const item of items) {
							if (Math.abs(lastUpdated - item.updated) < 70_000) {
								containerIds.add(item.id)
								newItems.push(item)
							}
						}
						for (const item of curItems ?? []) {
							if (!containerIds.has(item.id) && lastUpdated - item.updated < 70_000) {
								newItems.push(item)
							}
						}
						return newItems
					})
				})
		}

		// initial load
		fetchData(systemId)

		// if no systemId, pull system containers after every system update
		if (!systemId) {
			return $allSystemsById.listen((_value, _oldValue, systemId) => {
				// exclude initial load of systems
				if (Date.now() - loadTime > 500) {
					fetchData(systemId)
				}
			})
		}

		// if systemId, fetch containers after the system is updated
		return listenKeys($allSystemsById, [systemId], (_newSystems) => {
			fetchData(systemId)
		})
	}, [])

	const table = useReactTable({
		data: data ?? [],
		columns: containerChartCols.filter((col) => (systemId ? col.id !== "system" : true)),
		getCoreRowModel: getCoreRowModel(),
		getSortedRowModel: getSortedRowModel(),
		getFilteredRowModel: getFilteredRowModel(),
		onSortingChange: setSorting,
		onColumnFiltersChange: setColumnFilters,
		onColumnVisibilityChange: setColumnVisibility,
		onRowSelectionChange: setRowSelection,
		defaultColumn: {
			sortUndefined: "last",
			size: 100,
			minSize: 0,
		},
		state: {
			sorting,
			columnFilters,
			columnVisibility,
			rowSelection,
			globalFilter,
		},
		onGlobalFilterChange: setGlobalFilter,
		globalFilterFn: (row, _columnId, filterValue) => {
			const container = row.original
			const systemName = $allSystemsById.get()[container.system]?.name ?? ""
			const id = container.id ?? ""
			const name = container.name ?? ""
			const status = container.status ?? ""
			const healthLabel = ContainerHealthLabels[container.health as ContainerHealth] ?? ""
			const image = container.image ?? ""
			const ports = container.ports ?? ""
			const searchString = `${systemName} ${id} ${name} ${healthLabel} ${status} ${image} ${ports}`.toLowerCase()

			return (filterValue as string)
				.toLowerCase()
				.split(" ")
				.every((term) => searchString.includes(term))
		},
	})

	const rows = table.getRowModel().rows
	const visibleColumns = table.getVisibleLeafColumns()

	return (
		<Card className="p-6 @container w-full">
			<CardHeader className="p-0 mb-4">
				<div className="grid md:flex gap-5 w-full items-end">
					<div className="px-2 sm:px-1">
						<CardTitle className="mb-2">
							<Trans>All Containers</Trans>
						</CardTitle>
						<CardDescription className="flex">
							<Trans>Click on a container to view more information.</Trans>
						</CardDescription>
					</div>
					<div className="relative ms-auto w-full max-w-full md:w-64">
						<Input
							placeholder={t`Filter...`}
							value={globalFilter}
							onChange={(e) => setGlobalFilter(e.target.value)}
							className="ps-4 pe-10 w-full"
						/>
						{globalFilter && (
							<Button
								type="button"
								variant="ghost"
								size="icon"
								aria-label={t`Clear`}
								className="absolute right-1 top-1/2 -translate-y-1/2 h-7 w-7 text-muted-foreground"
								onClick={() => setGlobalFilter("")}
							>
								<XIcon className="h-4 w-4" />
							</Button>
						)}
					</div>
				</div>
			</CardHeader>
			<div className="rounded-md">
				<AllContainersTable table={table} rows={rows} colLength={visibleColumns.length} data={data} />
			</div>
		</Card>
	)
}

const AllContainersTable = memo(function AllContainersTable({
	table,
	rows,
	colLength,
	data,
}: {
	table: TableType<ContainerRecord>
	rows: Row<ContainerRecord>[]
	colLength: number
	data: ContainerRecord[] | undefined
}) {
	// The virtualizer will need a reference to the scrollable container element
	const scrollRef = useRef<HTMLDivElement>(null)
	const activeContainer = useRef<ContainerRecord | null>(null)
	const [sheetOpen, setSheetOpen] = useState(false)
	const openSheet = (container: ContainerRecord) => {
		activeContainer.current = container
		setSheetOpen(true)
	}

	const virtualizer = useVirtualizer<HTMLDivElement, HTMLTableRowElement>({
		count: rows.length,
		estimateSize: () => 54,
		getScrollElement: () => scrollRef.current,
		overscan: 5,
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
			{/* add header height to table size */}
			<div style={{ height: `${virtualizer.getTotalSize() + 48}px`, paddingTop, paddingBottom }}>
				<table className="text-sm w-full h-full text-nowrap">
					<ContainersTableHead table={table} />
					<TableBody>
						{rows.length ? (
							virtualRows.map((virtualRow) => {
								const row = rows[virtualRow.index]
								return <ContainerTableRow key={row.id} row={row} virtualRow={virtualRow} openSheet={openSheet} />
							})
						) : (
							<TableRow>
								<TableCell colSpan={colLength} className="h-37 text-center pointer-events-none">
									{data ? (
										<Trans>No results.</Trans>
									) : (
										<LoaderCircleIcon className="animate-spin size-10 opacity-60 mx-auto" />
									)}
								</TableCell>
							</TableRow>
						)}
					</TableBody>
				</table>
			</div>
			<ContainerSheet sheetOpen={sheetOpen} setSheetOpen={setSheetOpen} activeContainer={activeContainer} />
		</div>
	)
})

async function getLogsHtml(container: ContainerRecord): Promise<string> {
	try {
		const [{ highlighter }, logsHtml] = await Promise.all([
			import("@/lib/shiki"),
			pb.send<{ logs: string }>("/api/beszel/containers/logs", {
				system: container.system,
				container: container.id,
			}),
		])
		return logsHtml.logs ? highlighter.codeToHtml(logsHtml.logs, { lang: "log", theme: syntaxTheme }) : t`No results.`
	} catch (error) {
		console.error(error)
		return ""
	}
}

async function getInfoHtml(container: ContainerRecord): Promise<string> {
	try {
		let [{ highlighter }, { info }] = await Promise.all([
			import("@/lib/shiki"),
			pb.send<{ info: string }>("/api/beszel/containers/info", {
				system: container.system,
				container: container.id,
			}),
		])
		try {
			info = JSON.stringify(JSON.parse(info), null, 2)
		} catch (_) {}
		return info ? highlighter.codeToHtml(info, { lang: "json", theme: syntaxTheme }) : t`No results.`
	} catch (error) {
		console.error(error)
		return ""
	}
}

function ContainerSheet({
	sheetOpen,
	setSheetOpen,
	activeContainer,
}: {
	sheetOpen: boolean
	setSheetOpen: (open: boolean) => void
	activeContainer: RefObject<ContainerRecord | null>
}) {
	const [logsDisplay, setLogsDisplay] = useState<string>("")
	const [infoDisplay, setInfoDisplay] = useState<string>("")
	const [logsFullscreenOpen, setLogsFullscreenOpen] = useState<boolean>(false)
	const [infoFullscreenOpen, setInfoFullscreenOpen] = useState<boolean>(false)
	const [transferTaskOpen, setTransferTaskOpen] = useState<boolean>(false)
	const [isRefreshingLogs, setIsRefreshingLogs] = useState<boolean>(false)
	const logsContainerRef = useRef<HTMLDivElement>(null)

	const container = activeContainer.current

	function scrollLogsToBottom() {
		if (logsContainerRef.current) {
			logsContainerRef.current.scrollTo({ top: logsContainerRef.current.scrollHeight })
		}
	}

	const refreshLogs = async () => {
		if (!container) return
		setIsRefreshingLogs(true)
		const startTime = Date.now()

		try {
			const logsHtml = await getLogsHtml(container)
			setLogsDisplay(logsHtml)
			setTimeout(scrollLogsToBottom, 20)
		} catch (error) {
			console.error(error)
		} finally {
			// Ensure minimum spin duration of 800ms
			const elapsed = Date.now() - startTime
			const remaining = Math.max(0, 500 - elapsed)
			setTimeout(() => {
				setIsRefreshingLogs(false)
			}, remaining)
		}
	}

	useEffect(() => {
		setLogsDisplay("")
		setInfoDisplay("")
		if (!container) return
		;(async () => {
			const [logsHtml, infoHtml] = await Promise.all([getLogsHtml(container), getInfoHtml(container)])
			setLogsDisplay(logsHtml)
			setInfoDisplay(infoHtml)
			setTimeout(scrollLogsToBottom, 20)
		})()
	}, [container])

	if (!container) return null

	return (
		<>
			<LogsFullscreenDialog
				open={logsFullscreenOpen}
				onOpenChange={setLogsFullscreenOpen}
				logsDisplay={logsDisplay}
				containerName={container.name}
				onRefresh={refreshLogs}
				isRefreshing={isRefreshingLogs}
			/>
			<InfoFullscreenDialog
				open={infoFullscreenOpen}
				onOpenChange={setInfoFullscreenOpen}
				infoDisplay={infoDisplay}
				containerName={container.name}
			/>
			<TransferTaskDialog open={transferTaskOpen} onOpenChange={setTransferTaskOpen} container={container} />
			<Sheet open={sheetOpen} onOpenChange={setSheetOpen}>
				<SheetContent className="w-full sm:max-w-220 p-2">
					<SheetHeader>
						<SheetTitle>{container.name}</SheetTitle>
						<SheetDescription className="flex flex-wrap items-center gap-x-2 gap-y-1">
							<Link className="hover:underline" href={getPagePath($router, "system", { id: container.system })}>
								{$allSystemsById.get()[container.system]?.name ?? ""}
							</Link>
							<Separator orientation="vertical" className="h-2.5 bg-muted-foreground opacity-70" />
							{container.status}
							<Separator orientation="vertical" className="h-2.5 bg-muted-foreground opacity-70" />
							{container.image}
							<Separator orientation="vertical" className="h-2.5 bg-muted-foreground opacity-70" />
							{container.id}
							{/* {container.ports && (
								<>
									<Separator orientation="vertical" className="h-2.5 bg-muted-foreground opacity-70" />
									{container.ports}
								</>
							)} */}
							{/* <Separator orientation="vertical" className="h-2.5 bg-muted-foreground opacity-70" />
							{ContainerHealthLabels[container.health as ContainerHealth]} */}
						</SheetDescription>
					</SheetHeader>
					<div className="px-3 pb-3 -mt-4 flex flex-col gap-3 h-full items-start">
						<div className="flex w-full justify-end">
							<Button variant="outline" size="sm" onClick={() => setTransferTaskOpen(true)} disabled={!isAdmin()}>
								迁移任务
							</Button>
						</div>
						<div className="flex items-center w-full">
							<h3>{t`Logs`}</h3>
							<Button
								variant="ghost"
								size="sm"
								onClick={refreshLogs}
								className="h-8 w-8 p-0 ms-auto"
								disabled={isRefreshingLogs}
							>
								<RefreshCwIcon
									className={`size-4 transition-transform duration-300 ${isRefreshingLogs ? "animate-spin" : ""}`}
								/>
							</Button>
							<Button variant="ghost" size="sm" onClick={() => setLogsFullscreenOpen(true)} className="h-8 w-8 p-0">
								<MaximizeIcon className="size-4" />
							</Button>
						</div>
						<div
							ref={logsContainerRef}
							className={cn(
								"max-h-[calc(50dvh-10rem)] w-full overflow-auto p-3 rounded-md bg-gh-dark text-white text-sm",
								!logsDisplay && ["animate-pulse", "h-full"]
							)}
						>
							<div dangerouslySetInnerHTML={{ __html: logsDisplay }} />
						</div>
						<div className="flex items-center w-full">
							<h3>{t`Detail`}</h3>
							<Button
								variant="ghost"
								size="sm"
								onClick={() => setInfoFullscreenOpen(true)}
								className="h-8 w-8 p-0 ms-auto"
							>
								<MaximizeIcon className="size-4" />
							</Button>
						</div>
						<div
							className={cn(
								"grow h-[calc(50dvh-4rem)] w-full overflow-auto p-3 rounded-md bg-gh-dark text-white text-sm",
								!infoDisplay && "animate-pulse"
							)}
						>
							<div dangerouslySetInnerHTML={{ __html: infoDisplay }} />
						</div>
					</div>
				</SheetContent>
			</Sheet>
		</>
	)
}

function formatDockerTransferTime(value?: string) {
	if (!value) return ""
	const date = new Date(value)
	if (Number.isNaN(date.getTime())) {
		return value
	}
	return date.toLocaleString("zh-CN", { hour12: false })
}

function getDockerTransferStatusLabel(status?: string) {
	switch (status) {
		case "pending":
			return "排队中"
		case "validating":
			return "校验中"
		case "running":
			return "迁移中"
		case "done":
			return "已完成"
		case "failed":
			return "失败"
		default:
			return "等待中"
	}
}

function getDockerTransferStatusClass(status?: string) {
	switch (status) {
		case "done":
			return "border-emerald-500/30 bg-emerald-500/10 text-emerald-700"
		case "failed":
			return "border-destructive/30 bg-destructive/10 text-destructive"
		case "running":
		case "validating":
			return "border-sky-500/30 bg-sky-500/10 text-sky-700"
		default:
			return "border-muted bg-muted/40 text-muted-foreground"
	}
}

function getDockerTransferTaskHint(status: DockerTransferTaskStatusResponse | null) {
	const combinedText = [
		status?.progress?.detail ?? "",
		status?.result?.message ?? "",
		...(status?.logTail ?? []),
	]
		.join("\n")
		.trim()

	if (!combinedText) {
		return ""
	}

	if (/Permission denied \(publickey,password\)/i.test(combinedText)) {
		return "中转机还没有成功通过 SSH 登录这台服务器。请检查服务器用户、主机 IP、SSH 可达性，以及 Hub 公钥是否已经加入目标用户的 authorized_keys。"
	}
	if (/connect to host .* port 22: Connection timed out|No route to host/i.test(combinedText)) {
		return "目标机器可能不能被中转机直接访问。如果这台服务器需要跳板机，请开启“使用跳板机”开关，让 worker 从 relay 机 ~/.ssh/config 里查找对应的 Host 别名。"
	}
	if (/sudo 密码校验失败|无 sudo 权限/.test(combinedText)) {
		return "SSH 已连通，但 sudo 密码不正确，或者当前用户没有 sudo 权限。"
	}
	if (/缺少 docker/.test(combinedText)) {
		return "对应服务器缺少 docker，或者 docker 不在当前 SSH 用户的 PATH 中。"
	}
	if (/缺少 skopeo/.test(combinedText)) {
		return "对应服务器缺少 skopeo，迁移镜像前需要先安装它。"
	}
	if (/缺少 rsync/.test(combinedText)) {
		return "对应服务器缺少 rsync。中转机会通过 rsync 拉取和推送镜像目录，所以源服务器和目标服务器都必须安装 rsync。"
	}
	if (/目标镜像已存在/.test(combinedText)) {
		return "目标镜像已存在。可以开启“允许覆盖目标镜像”，或者换一个新的目标镜像名称。"
	}
	if (/目标容器已存在/.test(combinedText)) {
		return "目标容器已存在。可以开启“自动启动前删除同名目标容器”，或者换一个新的目标容器名。"
	}
	if (/目标端口已被占用/.test(combinedText)) {
		return "目标启动端口已经被占用。请改一个端口，或先释放目标服务器上的该端口。"
	}
	if (/创建目标挂载目录失败/.test(combinedText)) {
		return "目标挂载目录创建失败。通常是目录权限不足，或者目标服务器密码 / sudo 权限不正确。"
	}
	if (/额外 docker run 参数语法错误/.test(combinedText)) {
		return "额外 docker run 参数里存在引号或空格语法错误，worker 无法正确拆分命令。"
	}
	if (/镜像大小超过限制|镜像大小超限/.test(combinedText)) {
		return "commit 后的镜像大小超过当前允许上限，任务已被拦截。可以调大 Hub 的 `BESZEL_HUB_DOCKER_TRANSFER_MAX_IMAGE_SIZE_GB` 后再试。"
	}

	return ""
}

function splitDockerTransferSSHNode(value?: string) {
	const raw = value?.trim() ?? ""
	if (!raw) {
		return { user: "", host: "" }
	}
	if (raw.includes("@")) {
		const separatorIndex = raw.indexOf("@")
		return {
			user: raw.slice(0, separatorIndex).trim(),
			host: raw.slice(separatorIndex + 1).trim(),
		}
	}
	return { user: "", host: raw }
}

function buildDockerTransferCompletionSummary(status: DockerTransferTaskStatusResponse | null) {
	if (!status || status.queueState !== "done") {
		return ""
	}

	const sourceTarget = splitDockerTransferSSHNode(status.sourceNode)
	const destTarget = splitDockerTransferSSHNode(status.destNode)
	const sourceHost = sourceTarget.host || status.sourceNode?.trim() || "-"
	const destHost = destTarget.host || status.destNode?.trim() || "-"
	const targetImage = status.targetImage?.trim() || "-"
	const targetContainer = status.targetContainer?.trim() || "-"
	const port = status.port?.trim() || ""
	const autoStart = status.autoStart === true
	const mountBindings = (status.mountBindings ?? []).filter((binding) => binding.innerPath || binding.destVolDir)
	const vscodeHostAlias = `${targetContainer || "docker"}-${destHost.split(".").at(-1) || "remote"}`
	const lines = [
		"Docker 迁移已完成，请把下面信息发给用户：",
		`源服务器: ${sourceHost}`,
		`目标服务器: ${destHost}`,
		`目标镜像: ${targetImage}`,
		`目标容器: ${targetContainer}`,
		`自动启动: ${autoStart ? "是" : "否"}`,
	]

	if (autoStart && port) {
		lines.push(`容器 SSH 端口: ${port}`)
		lines.push(`容器 SSH 命令: ssh -p ${port} root@${destHost}`)
		lines.push("VS Code SSH 配置示例:")
		lines.push(`Host ${vscodeHostAlias}`)
		lines.push(`  HostName ${destHost}`)
		lines.push("  User root")
		lines.push(`  Port ${port}`)
	} else if (autoStart) {
		lines.push("容器 SSH 端口: 本次自动启动未额外指定 sshd 端口，请按镜像自身启动方式确认。")
	} else {
		lines.push("容器 SSH 端口: 本次任务只导入镜像，没有自动启动容器。")
	}

	if (mountBindings.length > 0) {
		lines.push("")
		lines.push("挂载目录说明:")
		for (const [index, binding] of mountBindings.entries()) {
			lines.push(`${index + 1}. ${binding.innerPath || "-"} 目录在新容器里是挂载到宿主机上的，对应源容器的${binding.innerPath || "-"}目录。`)
			lines.push("   请选择需要的数据通过 scp 命令传输到新容器。")
			if (binding.innerPath && autoStart && port) {
				lines.push(`   scp 示例: scp -P ${port} -r ${binding.innerPath}/[需要的源服务器文件] root@${destHost}:${binding.innerPath}/`)
			} else if (binding.destVolDir) {
				lines.push(`   目标宿主机实际目录: ${binding.destVolDir}`)
			}
			lines.push("   请把命令里的 [需要的源服务器文件] 替换成你要拷贝到新容器里的实际文件或目录。")
		}
	} else if (status.setupVolume) {
		lines.push("")
		lines.push("挂载目录说明:")
		lines.push("本次任务启用了挂载目录处理，但当前状态里没有拿到具体映射，请回到迁移任务参数里检查。")
	} else {
		lines.push("")
		lines.push("挂载目录说明:")
		lines.push("本次迁移任务没有配置额外的挂载目录映射。")
	}

	lines.push("")
	lines.push("提醒: docker commit 只会带走容器文件系统里的内容，宿主机挂载目录和独立 volume 里的数据不会自动一起迁移。")

	return lines.join("\n")
}

function TransferTaskDialog({
	open,
	onOpenChange,
	container,
}: {
	open: boolean
	onOpenChange: (open: boolean) => void
	container: ContainerRecord
}) {
	const systems = useStore($systems)
	const systemsById = useStore($allSystemsById)
	const currentSystem = systemsById[container.system]
	const targetSystems = systems.filter((system) => system.id !== container.system)
	const [targetSystemId, setTargetSystemId] = useState("")
	const [sourceUser, setSourceUser] = useState("")
	const [sourceNode, setSourceNode] = useState("")
	const [targetUser, setTargetUser] = useState("")
	const [useJumpHost, setUseJumpHost] = useState(false)
	const [containerName, setContainerName] = useState("")
	const [targetImage, setTargetImage] = useState("")
	const [allowTargetImageOverwrite, setAllowTargetImageOverwrite] = useState(false)
	const [setupVolume, setSetupVolume] = useState(false)
	const [mountBindings, setMountBindings] = useState<DockerTransferMountBinding[]>([{ innerPath: "", destVolDir: "" }])
	const [autoStart, setAutoStart] = useState(true)
	const [port, setPort] = useState("")
	const [targetContainer, setTargetContainer] = useState("")
	const [extraRunArgs, setExtraRunArgs] = useState("")
	const [sourceSudoPassword, setSourceSudoPassword] = useState("")
	const [destSudoPassword, setDestSudoPassword] = useState("")
	const [removeExistingContainer, setRemoveExistingContainer] = useState(false)
	const [keepLocalCache, setKeepLocalCache] = useState(false)
	const [shmSize, setShmSize] = useState("1500G")
	const [relayConfig, setRelayConfig] = useState<DockerTransferConfig>({
		enabled: true,
		relayHost: dockerTransferRelayDefaults.host,
		relayDir: dockerTransferRelayDefaults.dir,
		queueRoot: dockerTransferRelayDefaults.dir,
		maxImageSizeGb: 200,
	})
	const [isLoadingConfig, setIsLoadingConfig] = useState(false)
	const [isSubmitting, setIsSubmitting] = useState(false)
	const [progressDialogOpen, setProgressDialogOpen] = useState(false)
	const [submittedTaskId, setSubmittedTaskId] = useState("")
	const [taskStatus, setTaskStatus] = useState<DockerTransferTaskStatusResponse | null>(null)
	const [taskStatusError, setTaskStatusError] = useState("")
	const [isLoadingTaskStatus, setIsLoadingTaskStatus] = useState(false)
	const initKeyRef = useRef("")

	useEffect(() => {
		if (!open) {
			initKeyRef.current = ""
			return
		}

		const defaultTargetSystem = targetSystems[0]
		const initKey = `${container.id}:${currentSystem?.id ?? ""}:${defaultTargetSystem?.id ?? ""}`
		if (initKeyRef.current === initKey) {
			return
		}

		setTargetSystemId(defaultTargetSystem?.id ?? "")
		setSourceUser(getDefaultNodeUser(currentSystem))
		setSourceNode(getDefaultNodeHost(currentSystem))
		setTargetUser(getDefaultNodeUser(defaultTargetSystem))
		setUseJumpHost(false)
		setContainerName(container.name)
		setTargetImage(getDefaultTargetImage(container))
		setAllowTargetImageOverwrite(false)
		setSetupVolume(false)
		setMountBindings([{ innerPath: "", destVolDir: "" }])
		setAutoStart(true)
		setPort("")
		setTargetContainer(container.name)
		setExtraRunArgs("")
		setSourceSudoPassword("")
		setDestSudoPassword("")
		setRemoveExistingContainer(false)
		setKeepLocalCache(false)
		setShmSize("1500G")
		initKeyRef.current = initKey
	}, [open, container.id, currentSystem?.id, currentSystem?.host, targetSystems])

	useEffect(() => {
		if (!open || !isAdmin()) return

		let cancelled = false
		setIsLoadingConfig(true)

		pb.send<DockerTransferConfig>("/api/beszel/docker-transfer/config", {})
			.then((config) => {
				if (!cancelled) {
					setRelayConfig(config)
				}
			})
			.catch((error: unknown) => {
				console.error(error)
				if (!cancelled) {
					toast({
						title: "获取中转配置失败",
						description: (error as Error).message,
						variant: "destructive",
					})
				}
			})
			.finally(() => {
				if (!cancelled) {
					setIsLoadingConfig(false)
				}
			})

		return () => {
			cancelled = true
		}
	}, [open])

	useEffect(() => {
		if (!progressDialogOpen || !submittedTaskId || !isAdmin()) return

		let cancelled = false
		let timer: ReturnType<typeof setTimeout> | null = null

		const pollStatus = async (showLoader: boolean) => {
			if (showLoader && !cancelled) {
				setIsLoadingTaskStatus(true)
			}

			try {
				const status = await pb.send<DockerTransferTaskStatusResponse>(
					`/api/beszel/docker-transfer/task-status?taskId=${encodeURIComponent(submittedTaskId)}`,
					{}
				)
				if (cancelled) return

				setTaskStatus(status)
				setTaskStatusError("")

				const isTerminal = status.queueState === "done" || status.queueState === "failed"
				if (!isTerminal) {
					timer = setTimeout(() => {
						void pollStatus(false)
					}, 1500)
				}
			} catch (error: unknown) {
				if (cancelled) return
				setTaskStatusError((error as Error).message)
				timer = setTimeout(() => {
					void pollStatus(false)
				}, 3000)
			} finally {
				if (!cancelled) {
					setIsLoadingTaskStatus(false)
				}
			}
		}

		void pollStatus(true)

		return () => {
			cancelled = true
			if (timer) {
				clearTimeout(timer)
			}
		}
	}, [progressDialogOpen, submittedTaskId])

	const targetSystem = targetSystems.find((system) => system.id === targetSystemId)
	const targetNode = getDefaultNodeHost(targetSystem)
	const normalizedMountBindings = mountBindings.map((binding) => ({
		innerPath: binding.innerPath.trim(),
		destVolDir: binding.destVolDir.trim(),
	}))
	const hasRequiredFields =
		sourceUser.trim() !== "" &&
		sourceNode.trim() !== "" &&
		targetSystemId.trim() !== "" &&
		targetUser.trim() !== "" &&
		targetNode.trim() !== "" &&
		containerName.trim() !== "" &&
		targetImage.trim() !== "" &&
		(!setupVolume ||
			(normalizedMountBindings.length > 0 &&
				normalizedMountBindings.every((binding) => binding.innerPath !== "" && binding.destVolDir !== "")))

	const taskPayload: DockerTransferTaskPayload | null = hasRequiredFields
		? {
				sourceSystemId: currentSystem?.id,
				sourceSystemName: currentSystem?.name,
				sourceUser: sourceUser.trim(),
				destSystemId: targetSystem?.id,
				destSystemName: targetSystem?.name,
				destUser: targetUser.trim(),
				sourceNode: sourceNode.trim(),
				destNode: targetNode.trim(),
				useJumpHost,
				containerName: containerName.trim(),
				targetImage: targetImage.trim(),
				allowTargetImageOverwrite,
				setupVolume,
				mountBindings: normalizedMountBindings,
				innerPath: normalizedMountBindings[0]?.innerPath ?? "",
				destVolDir: normalizedMountBindings[0]?.destVolDir ?? "",
				autoStart,
				port: port.trim(),
				targetContainer: targetContainer.trim(),
				extraRunArgs: extraRunArgs.trim(),
				sourceSudoPassword,
				destSudoPassword,
				removeExistingContainer,
				keepLocalCache,
				shmSize: shmSize.trim() || "1500G",
			}
		: null

	const taskPreview = taskPayload ? buildDockerTransferTaskPreview(taskPayload) : "{\n  \"error\": \"请先填写完整的迁移参数\"\n}"
	const hasConflictingAutoStartArgs =
		port.trim() !== "" && /--entrypoint|\bsshd\b/i.test(extraRunArgs)
	const validationMessages = [
		!sourceUser.trim() ? "请填写源服务器用户。" : "",
		!sourceNode.trim() ? "请填写源服务器主机/IP。" : "",
		!targetSystemId.trim() ? "请选择目标服务器。" : "",
		targetSystemId.trim() !== "" && !targetNode.trim() ? "目标服务器没有可用的主机/IP，请先在系统配置里补全 host。" : "",
		!targetUser.trim() ? "请填写目标服务器用户。" : "",
		!containerName.trim() ? "请填写源容器名称。" : "",
		!targetImage.trim() ? "请填写目标镜像名称。" : "",
		setupVolume && normalizedMountBindings.some((binding) => !binding.innerPath || !binding.destVolDir)
			? "启用挂载目录同步时，每一条映射都必须同时填写容器内目录和目标服务器目录。"
			: "",
		hasConflictingAutoStartArgs ? "填写了启动端口时，额外 docker run 参数里不能再包含 --entrypoint 或 sshd。" : "",
	].filter(Boolean)
	const currentTaskState = taskStatus?.progress?.state || taskStatus?.queueState || "pending"
	const currentTaskLabel = getDockerTransferStatusLabel(currentTaskState)
	const currentTaskHint = getDockerTransferTaskHint(taskStatus)
	const progressHistory = taskStatus?.progress?.history ?? []
	const hasTerminalTaskState = taskStatus?.queueState === "done" || taskStatus?.queueState === "failed"
	const completionSummary = buildDockerTransferCompletionSummary(taskStatus)

	function updateMountBinding(index: number, key: keyof DockerTransferMountBinding, value: string) {
		setMountBindings((current) =>
			current.map((binding, bindingIndex) => (bindingIndex === index ? { ...binding, [key]: value } : binding))
		)
	}

	function addMountBinding() {
		setMountBindings((current) => [...current, { innerPath: "", destVolDir: "" }])
	}

	function removeMountBinding(index: number) {
		setMountBindings((current) => {
			if (current.length <= 1) {
				return [{ innerPath: "", destVolDir: "" }]
			}
			return current.filter((_, bindingIndex) => bindingIndex !== index)
		})
	}

	async function submitTask() {
		if (!taskPayload || hasConflictingAutoStartArgs) {
			if (hasConflictingAutoStartArgs) {
				toast({
					title: "参数冲突",
					description: "填写了启动端口时，额外 docker run 参数里不能再包含 --entrypoint 或 sshd。",
					variant: "destructive",
				})
			}
			return
		}
		setIsSubmitting(true)
		try {
			const res = await pb.send<DockerTransferTaskResponse>("/api/beszel/docker-transfer/tasks", {
				method: "POST",
				body: taskPayload,
			})
			setSubmittedTaskId(res.taskId)
			setTaskStatus({
				found: true,
				taskId: res.taskId,
				queueState: "pending",
				sourceNode: taskPayload.sourceNode,
				destNode: taskPayload.destNode,
				containerName: taskPayload.containerName,
				targetImage: taskPayload.targetImage,
				targetContainer: taskPayload.targetContainer,
				progress: {
					state: "pending",
					stage: "等待执行",
					message: "任务已提交，等待 worker 领取",
				},
				logTail: [],
			})
			setTaskStatusError("")
			setProgressDialogOpen(true)
			onOpenChange(false)
		} catch (error: unknown) {
			console.error(error)
			toast({
				title: "提交迁移任务失败",
				description: (error as Error).message,
				variant: "destructive",
			})
		} finally {
			setIsSubmitting(false)
		}
	}

	return (
		<>
			<Dialog open={progressDialogOpen} onOpenChange={setProgressDialogOpen}>
				<DialogContent className="max-w-3xl max-h-[88vh] overflow-auto">
					<DialogHeader>
						<DialogTitle>迁移任务进度</DialogTitle>
						<DialogDescription>
							任务 ID: {submittedTaskId || "-"}
							<br />
							源服务器: {taskStatus?.sourceNode || taskPayload?.sourceNode || "-"}
							<br />
							目标服务器: {taskStatus?.destNode || taskPayload?.destNode || "-"}
						</DialogDescription>
					</DialogHeader>
					<div className="grid gap-4">
						<div className={cn("rounded-md border p-4", getDockerTransferStatusClass(currentTaskState))}>
							<div className="flex items-start gap-3">
								{currentTaskState === "done" ? (
									<CheckCircle2Icon className="mt-0.5 size-5 shrink-0" />
								) : currentTaskState === "failed" ? (
									<AlertCircleIcon className="mt-0.5 size-5 shrink-0" />
								) : isLoadingTaskStatus ? (
									<LoaderCircleIcon className="mt-0.5 size-5 shrink-0 animate-spin" />
								) : (
									<Clock3Icon className="mt-0.5 size-5 shrink-0" />
								)}
								<div className="grid gap-1">
									<div className="font-medium">{currentTaskLabel}</div>
									<div className="text-sm">{taskStatus?.progress?.stage || "等待 worker 处理"}</div>
									<div className="text-sm opacity-90">{taskStatus?.progress?.message || "任务已提交，正在等待状态更新。"}</div>
									{taskStatus?.progress?.updated_at && (
										<div className="text-xs opacity-70">更新时间: {formatDockerTransferTime(taskStatus.progress.updated_at)}</div>
									)}
								</div>
							</div>
						</div>
						{taskStatusError && (
							<div className="rounded-md border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive">
								状态查询失败: {taskStatusError}
							</div>
						)}
						{currentTaskHint && (
							<div className="rounded-md border border-amber-500/30 bg-amber-500/10 p-3 text-sm text-amber-700">
								提示: {currentTaskHint}
							</div>
						)}
						<div className="grid gap-2">
							<div className="font-medium">当前任务信息</div>
							<div className="grid gap-2 rounded-md border p-3 text-sm">
								<div>源容器: {taskStatus?.containerName || taskPayload?.containerName || "-"}</div>
								<div>目标镜像: {taskStatus?.targetImage || taskPayload?.targetImage || "-"}</div>
								<div>目标容器: {taskStatus?.targetContainer || taskPayload?.targetContainer || "-"}</div>
							</div>
						</div>
						<div className="grid gap-2">
							<div className="font-medium">执行时间线</div>
							<div className="grid gap-2 rounded-md border p-3">
								{progressHistory.length ? (
									progressHistory.map((entry, index) => (
										<div key={`${entry.at}-${index}`} className="grid gap-1 border-b pb-2 last:border-b-0 last:pb-0">
											<div className="flex items-center justify-between gap-2 text-sm">
												<span className="font-medium">{entry.stage || getDockerTransferStatusLabel(entry.state)}</span>
												<span className="text-muted-foreground">{formatDockerTransferTime(entry.at)}</span>
											</div>
											<div className="text-sm text-muted-foreground">{entry.message || "-"}</div>
										</div>
									))
								) : (
									<div className="text-sm text-muted-foreground">worker 还没有写入进度，通常表示任务刚提交，或正在等待轮询到这条任务。</div>
								)}
							</div>
						</div>
						<div className="grid gap-2">
							<div className="font-medium">最近日志</div>
							<div className="rounded-md border bg-muted/30 p-3">
								<pre className="max-h-72 overflow-auto whitespace-pre-wrap break-all text-xs leading-5">
									{taskStatus?.logTail?.length
										? taskStatus.logTail.join("\n")
										: "当前还没有日志输出。若任务已提交，通常再等 1-2 秒 worker 就会开始写日志。"}
								</pre>
							</div>
						</div>
						{completionSummary && (
							<div className="grid gap-2">
								<div className="flex items-center justify-between gap-3">
									<div className="font-medium">交付说明</div>
									<Button variant="outline" size="sm" onClick={() => copyToClipboard(completionSummary)}>
										复制交付说明
									</Button>
								</div>
								<Textarea readOnly value={completionSummary} className="min-h-[22rem] font-mono text-xs leading-5" />
							</div>
						)}
					</div>
					<DialogFooter>
						{!hasTerminalTaskState && (
							<div className="me-auto flex items-center gap-2 text-sm text-muted-foreground">
								<LoaderCircleIcon className="size-4 animate-spin" />
								正在轮询任务状态
							</div>
						)}
						<Button
							variant="outline"
							onClick={() => {
								if (taskStatus?.logTail?.length) {
									copyToClipboard(taskStatus.logTail.join("\n"))
								}
							}}
						>
							复制最近日志
						</Button>
						<Button onClick={() => setProgressDialogOpen(false)}>关闭</Button>
					</DialogFooter>
				</DialogContent>
			</Dialog>
			<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="max-w-5xl max-h-[92vh] overflow-auto">
				<DialogHeader>
					<DialogTitle>提交迁移任务</DialogTitle>
					<DialogDescription>
						提交后，Hub 会通过 SSH 把任务写入中转机队列，由常驻 worker 自动执行。
						<br />
						当前中转机: {relayConfig.relayUser ? `${relayConfig.relayUser}@` : ""}
						{relayConfig.relayHost || "当前 Hub 主机"}
						<br />
						队列目录: {relayConfig.queueRoot}
						<br />
						当前镜像大小上限: {relayConfig.maxImageSizeGb ?? 200} GB
						<br />
						请填写源服务器 SSH 用户与主机/IP，并选择目标服务器后填写其 SSH 用户。如果某台机器需要通过跳板机访问，请开启下面的“使用跳板机”选项。
						{isLoadingConfig && <span className="ms-2">正在读取配置...</span>}
					</DialogDescription>
				</DialogHeader>
				<div className="grid gap-4 md:grid-cols-2">
					<div className="grid gap-2">
						<Label htmlFor="transfer-source-user">源服务器用户</Label>
						<Input
							id="transfer-source-user"
							value={sourceUser}
							onChange={(e) => setSourceUser(e.target.value)}
							placeholder="例如 vihuman"
						/>
					</div>
					<div className="grid gap-2">
						<Label htmlFor="transfer-target-user">目标服务器用户</Label>
						<Input
							id="transfer-target-user"
							value={targetUser}
							onChange={(e) => setTargetUser(e.target.value)}
							placeholder="例如 vihuman"
						/>
					</div>
					<div className="grid gap-2">
						<Label htmlFor="transfer-source-node">源服务器主机/IP</Label>
						<Input
							id="transfer-source-node"
							value={sourceNode}
							onChange={(e) => setSourceNode(e.target.value)}
							placeholder="例如 10.15.88.84"
						/>
					</div>
					<div className="grid gap-2">
						<Label htmlFor="transfer-target-system">目标服务器</Label>
						<Select
							value={targetSystemId}
							onValueChange={(value) => {
								setTargetSystemId(value)
								const nextSystem = systemsById[value]
								const nextUser = getDefaultNodeUser(nextSystem)
								if (!targetUser.trim() || nextUser) {
									setTargetUser(nextUser)
								}
							}}
						>
							<SelectTrigger id="transfer-target-system">
								<SelectValue placeholder="选择目标服务器" />
							</SelectTrigger>
							<SelectContent>
								{targetSystems.map((system) => {
									const host = getDefaultNodeHost(system)
									return (
										<SelectItem key={system.id} value={system.id}>
											{host ? `${system.name} (${host})` : system.name}
										</SelectItem>
									)
								})}
							</SelectContent>
						</Select>
					</div>
					<div className="grid gap-2">
						<Label htmlFor="transfer-source-sudo-password">源服务器密码</Label>
						<Input
							id="transfer-source-sudo-password"
							type="password"
							value={sourceSudoPassword}
							onChange={(e) => setSourceSudoPassword(e.target.value)}
						/>
					</div>
					<div className="grid gap-2">
						<Label htmlFor="transfer-dest-sudo-password-inline">目标服务器密码</Label>
						<Input
							id="transfer-dest-sudo-password-inline"
							type="password"
							value={destSudoPassword}
							onChange={(e) => setDestSudoPassword(e.target.value)}
						/>
					</div>
					<div className="grid gap-2">
						<Label htmlFor="transfer-container-name">源容器名称</Label>
						<Input
							id="transfer-container-name"
							value={containerName}
							onChange={(e) => setContainerName(e.target.value)}
						/>
					</div>
					<div className="grid gap-2">
						<Label htmlFor="transfer-target-image">目标镜像名称</Label>
						<Input
							id="transfer-target-image"
							value={targetImage}
							onChange={(e) => setTargetImage(e.target.value)}
							placeholder="例如 my_algo:migrated"
						/>
					</div>
					<div className="grid gap-2">
						<Label htmlFor="transfer-target-container">目标容器名称</Label>
						<Input
							id="transfer-target-container"
							value={targetContainer}
							onChange={(e) => setTargetContainer(e.target.value)}
							placeholder="留空则默认使用源容器名称"
						/>
					</div>
					<div className="grid gap-2">
						<Label htmlFor="transfer-port">启动端口</Label>
						<Input
							id="transfer-port"
							value={port}
							onChange={(e) => setPort(e.target.value)}
							placeholder="留空则不额外指定 sshd 端口"
						/>
					</div>
					<div className="grid gap-2 md:col-span-2">
						<Label htmlFor="transfer-shm-size">共享内存大小</Label>
						<Input
							id="transfer-shm-size"
							value={shmSize}
							onChange={(e) => setShmSize(e.target.value)}
						/>
					</div>
					{autoStart && (
						<div className="grid gap-2 md:col-span-2">
							<Label htmlFor="transfer-extra-run-args">额外 docker run 参数</Label>
							<Textarea
								id="transfer-extra-run-args"
								value={extraRunArgs}
								onChange={(e) => setExtraRunArgs(e.target.value)}
								className="min-h-24 font-mono"
								placeholder='例如 --ipc=host -e HF_HOME=/data/hf --ulimit memlock=-1 --cap-add SYS_ADMIN'
							/>
							<div className="text-sm text-muted-foreground">
								仅在“导入后自动启动容器”时生效。会按 shell 风格拆分后追加到 docker run。
							</div>
							{hasConflictingAutoStartArgs && (
								<div className="text-sm text-destructive">
									填写了启动端口时，额外 docker run 参数里不能再包含 --entrypoint 或 sshd。
								</div>
							)}
						</div>
					)}
				</div>
				<div className="grid gap-4 md:grid-cols-2">
					<label className="flex items-center justify-between rounded-md border p-3">
						<div className="grid gap-1">
							<div className="font-medium">允许覆盖目标镜像</div>
							<div className="text-sm text-muted-foreground">目标服务器已有同名镜像时，允许继续导入</div>
						</div>
						<Switch checked={allowTargetImageOverwrite} onCheckedChange={setAllowTargetImageOverwrite} />
					</label>
					<label className="flex items-center justify-between rounded-md border p-3">
						<div className="grid gap-1">
							<div className="font-medium">导入后自动启动容器</div>
							<div className="text-sm text-muted-foreground">在目标服务器上直接执行 docker run</div>
						</div>
						<Switch checked={autoStart} onCheckedChange={setAutoStart} />
					</label>
					<label className="flex items-center justify-between rounded-md border p-3">
						<div className="grid gap-1">
							<div className="font-medium">使用跳板机</div>
							<div className="text-sm text-muted-foreground">根据 relay 机 ~/.ssh/config 自动查找源/目标主机对应的 Host 别名</div>
						</div>
						<Switch checked={useJumpHost} onCheckedChange={setUseJumpHost} />
					</label>
					<label className="flex items-center justify-between rounded-md border p-3">
						<div className="grid gap-1">
							<div className="font-medium">同步挂载目录</div>
							<div className="text-sm text-muted-foreground">把容器内目录映射到目标服务器目录</div>
						</div>
						<Switch checked={setupVolume} onCheckedChange={setSetupVolume} />
					</label>
					<label className="flex items-center justify-between rounded-md border p-3">
						<div className="grid gap-1">
							<div className="font-medium">保留中转缓存</div>
							<div className="text-sm text-muted-foreground">默认任务结束后会清理本地 cache</div>
						</div>
						<Switch checked={keepLocalCache} onCheckedChange={setKeepLocalCache} />
					</label>
					<label className="flex items-center justify-between rounded-md border p-3">
						<div className="grid gap-1">
							<div className="font-medium">自动启动前删除同名目标容器</div>
							<div className="text-sm text-muted-foreground">仅在启用自动启动时生效</div>
						</div>
						<Switch checked={removeExistingContainer} onCheckedChange={setRemoveExistingContainer} />
					</label>
				</div>
				{setupVolume && (
					<div className="grid gap-4">
						<div className="flex items-center justify-between">
							<div className="text-sm font-medium">挂载目录映射</div>
							<Button type="button" variant="outline" size="sm" onClick={addMountBinding}>
								新增一条挂载
							</Button>
						</div>
						{mountBindings.map((binding, index) => (
							<div key={`mount-binding-${index}`} className="grid gap-3 rounded-md border p-3">
								<div className="flex items-center justify-between">
									<div className="text-sm text-muted-foreground">挂载 {index + 1}</div>
									<Button
										type="button"
										variant="ghost"
										size="sm"
										onClick={() => removeMountBinding(index)}
										disabled={mountBindings.length === 1}
									>
										删除
									</Button>
								</div>
								<div className="grid gap-4 md:grid-cols-2">
									<div className="grid gap-2">
										<Label htmlFor={`transfer-inner-path-${index}`}>容器内目录</Label>
										<Input
											id={`transfer-inner-path-${index}`}
											value={binding.innerPath}
											onChange={(e) => updateMountBinding(index, "innerPath", e.target.value)}
											placeholder="例如 /data"
										/>
									</div>
									<div className="grid gap-2">
										<Label htmlFor={`transfer-dest-vol-dir-${index}`}>目标服务器目录</Label>
										<Input
											id={`transfer-dest-vol-dir-${index}`}
											value={binding.destVolDir}
											onChange={(e) => updateMountBinding(index, "destVolDir", e.target.value)}
											placeholder="例如 /data/containers/demo"
										/>
									</div>
								</div>
							</div>
						))}
					</div>
				)}
				<div className="grid gap-2">
					<Label htmlFor="transfer-preview">任务预览</Label>
					<Textarea
						id="transfer-preview"
						className="min-h-[18rem] font-mono whitespace-pre"
						readOnly
						value={taskPreview}
					/>
					{validationMessages.length > 0 && (
						<div className="grid gap-1 rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">
							{validationMessages.map((message) => (
								<div key={message}>{message}</div>
							))}
						</div>
					)}
				</div>
				<DialogFooter>
					<Button variant="outline" onClick={() => copyToClipboard(taskPreview)}>
						复制任务 JSON
					</Button>
					<Button onClick={submitTask} disabled={!hasRequiredFields || hasConflictingAutoStartArgs || isSubmitting}>
						{isSubmitting ? "提交中..." : "提交任务"}
					</Button>
				</DialogFooter>
			</DialogContent>
			</Dialog>
		</>
	)
}

function ContainersTableHead({ table }: { table: TableType<ContainerRecord> }) {
	return (
		<TableHeader className="sticky top-0 z-50 w-full border-b-2">
			<div className="absolute -top-2 left-0 w-full h-4 bg-table-header z-50"></div>
			{table.getHeaderGroups().map((headerGroup) => (
				<tr key={headerGroup.id}>
					{headerGroup.headers.map((header) => {
						return (
							<TableHead className="px-2" key={header.id} style={{ width: header.getSize() }}>
								{header.isPlaceholder ? null : flexRender(header.column.columnDef.header, header.getContext())}
							</TableHead>
						)
					})}
				</tr>
			))}
		</TableHeader>
	)
}

const ContainerTableRow = memo(function ContainerTableRow({
	row,
	virtualRow,
	openSheet,
}: {
	row: Row<ContainerRecord>
	virtualRow: VirtualItem
	openSheet: (container: ContainerRecord) => void
}) {
	return (
		<TableRow
			data-state={row.getIsSelected() && "selected"}
			className="cursor-pointer transition-opacity"
			onClick={() => openSheet(row.original)}
		>
			{row.getVisibleCells().map((cell) => (
				<TableCell
					key={cell.id}
					className="py-0 ps-4.5"
					style={{
						height: virtualRow.size,
						width: cell.column.getSize(),
					}}
				>
					{flexRender(cell.column.columnDef.cell, cell.getContext())}
				</TableCell>
			))}
		</TableRow>
	)
})

function LogsFullscreenDialog({
	open,
	onOpenChange,
	logsDisplay,
	containerName,
	onRefresh,
	isRefreshing,
}: {
	open: boolean
	onOpenChange: (open: boolean) => void
	logsDisplay: string
	containerName: string
	onRefresh: () => void | Promise<void>
	isRefreshing: boolean
}) {
	const outerContainerRef = useRef<HTMLDivElement>(null)

	useEffect(() => {
		if (open && logsDisplay) {
			// Scroll the outer container to bottom
			const scrollToBottom = () => {
				if (outerContainerRef.current) {
					outerContainerRef.current.scrollTop = outerContainerRef.current.scrollHeight
				}
			}
			setTimeout(scrollToBottom, 50)
		}
	}, [open, logsDisplay])

	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="w-[calc(100vw-20px)] h-[calc(100dvh-20px)] max-w-none p-0 bg-gh-dark border-0 text-white">
				<DialogTitle className="sr-only">{containerName} logs</DialogTitle>
				<div ref={outerContainerRef} className="h-full overflow-auto">
					<div className="h-full w-full px-3 leading-relaxed rounded-md bg-gh-dark text-sm">
						<div className="py-3" dangerouslySetInnerHTML={{ __html: logsDisplay }} />
					</div>
				</div>
				<button
					onClick={onRefresh}
					className="absolute top-3 right-11 opacity-60 hover:opacity-100 p-1"
					disabled={isRefreshing}
					title={t`Refresh`}
					aria-label={t`Refresh`}
				>
					<RefreshCwIcon className={`size-4 transition-transform duration-300 ${isRefreshing ? "animate-spin" : ""}`} />
				</button>
			</DialogContent>
		</Dialog>
	)
}

function InfoFullscreenDialog({
	open,
	onOpenChange,
	infoDisplay,
	containerName,
}: {
	open: boolean
	onOpenChange: (open: boolean) => void
	infoDisplay: string
	containerName: string
}) {
	return (
		<Dialog open={open} onOpenChange={onOpenChange}>
			<DialogContent className="w-[calc(100vw-20px)] h-[calc(100dvh-20px)] max-w-none p-0 bg-gh-dark border-0 text-white">
				<DialogTitle className="sr-only">{containerName} info</DialogTitle>
				<div className="flex-1 overflow-auto">
					<div className="h-full w-full overflow-auto p-3 rounded-md bg-gh-dark text-sm leading-relaxed">
						<div dangerouslySetInnerHTML={{ __html: infoDisplay }} />
					</div>
				</div>
			</DialogContent>
		</Dialog>
	)
}
