import type { ContainerRecord, SystemRecord } from "@/types"

export const dockerTransferRelayDefaults = {
	host: "10.19.131.195",
	dir: "/home/luhaotao/data/docker_tmp",
}

export interface DockerTransferMountBinding {
	innerPath: string
	destVolDir: string
}

export interface DockerTransferTaskPayload {
	sourceSystemId?: string
	sourceSystemName?: string
	destSystemId?: string
	destSystemName?: string
	sourceUser: string
	sourceNode: string
	destUser: string
	destNode: string
	useJumpHost: boolean
	containerName: string
	targetImage: string
	allowTargetImageOverwrite: boolean
	setupVolume: boolean
	mountBindings: DockerTransferMountBinding[]
	innerPath: string
	destVolDir: string
	autoStart: boolean
	port: string
	targetContainer: string
	extraRunArgs: string
	sourceSudoPassword: string
	destSudoPassword: string
	removeExistingContainer: boolean
	keepLocalCache: boolean
	shmSize: string
}

export interface DockerTransferConfig {
	enabled: boolean
	relayUser?: string
	relayHost: string
	relayDir: string
	queueRoot: string
	maxImageSizeGb?: number
}

export interface DockerTransferTaskResponse {
	success: boolean
	taskId: string
	queueFile: string
	pendingDir: string
	relayUser?: string
	relayHost: string
	relayDir: string
}

export interface DockerTransferTaskProgressEntry {
	at: string
	level?: string
	state?: string
	stage?: string
	message?: string
}

export interface DockerTransferTaskProgress {
	state: string
	stage?: string
	message?: string
	detail?: string
	updated_at?: string
	history?: DockerTransferTaskProgressEntry[]
}

export interface DockerTransferTaskResult {
	status: string
	message: string
	relay_host?: string
	log_file?: string
	started_at?: string
	finished_at?: string
	source_node?: string
	dest_node?: string
	container_name?: string
	target_image?: string
	target_container?: string
	local_cache_dir?: string
}

export interface DockerTransferTaskStatusResponse {
	found: boolean
	taskId: string
	queueState: string
	sourceNode?: string
	destNode?: string
	containerName?: string
	targetImage?: string
	targetContainer?: string
	setupVolume?: boolean
	mountBindings?: DockerTransferMountBinding[]
	autoStart?: boolean
	port?: string
	progress?: DockerTransferTaskProgress
	result?: DockerTransferTaskResult | null
	logTail?: string[]
}

export function getDefaultTargetImage(container: ContainerRecord) {
	return `${container.name}:migrated`
}

function splitSSHNode(value?: string) {
	const raw = value?.trim() ?? ""
	if (!raw) {
		return { user: "", host: "" }
	}
	if (raw.includes("@")) {
		const separatorIndex = raw.indexOf("@")
		const user = raw.slice(0, separatorIndex)
		const host = raw.slice(separatorIndex + 1)
		return {
			user: user?.trim() ?? "",
			host: host?.trim() ?? "",
		}
	}
	return { user: "", host: raw }
}

export function getDefaultNodeHost(system?: SystemRecord) {
	return splitSSHNode(system?.host).host
}

export function getDefaultNodeUser(system?: SystemRecord) {
	return splitSSHNode(system?.host).user
}

export function buildDockerTransferTaskPreview(payload: DockerTransferTaskPayload) {
	return JSON.stringify(
		{
			sourceSystemId: payload.sourceSystemId,
			sourceSystemName: payload.sourceSystemName,
			sourceUser: payload.sourceUser,
			destSystemId: payload.destSystemId,
			destSystemName: payload.destSystemName,
			destUser: payload.destUser,
			sourceNode: payload.sourceNode,
			destNode: payload.destNode,
			useJumpHost: payload.useJumpHost,
			containerName: payload.containerName,
			targetImage: payload.targetImage,
			allowTargetImageOverwrite: payload.allowTargetImageOverwrite,
			setupVolume: payload.setupVolume,
			mountBindings: payload.mountBindings,
			innerPath: payload.innerPath,
			destVolDir: payload.destVolDir,
			autoStart: payload.autoStart,
			port: payload.port,
			targetContainer: payload.targetContainer,
			extraRunArgs: payload.extraRunArgs,
			sourceSudoPassword: payload.sourceSudoPassword ? "***" : "",
			destSudoPassword: payload.destSudoPassword ? "***" : "",
			removeExistingContainer: payload.removeExistingContainer,
			keepLocalCache: payload.keepLocalCache,
			shmSize: payload.shmSize,
		},
		null,
		2
	)
}
