import { useCallback, useEffect, useRef, useState } from 'react'
import type { ChangeEvent, DragEvent, RefObject } from 'react'
import type { Terminal } from '@xterm/xterm'
import { useFileUpload } from '../../hooks/useFileUpload'

export type GestureDirection = 'up' | 'down' | 'left' | 'right'

interface UseTerminalInputOptions {
  sessionName: string
  hostId?: string
  termRef: RefObject<Terminal | null>
  sendRawBytes: (bytes: Uint8Array) => void
  sendText: (text: string) => void
  sendImage: (file: File, fallbackType: string) => void
  termConnected: boolean
  focus: () => void
}

export function useTerminalInput({
  sessionName,
  hostId,
  termRef,
  sendRawBytes,
  sendText,
  sendImage,
  termConnected,
  focus,
}: UseTerminalInputOptions) {
  const [capturedText, setCapturedText] = useState<string | null>(null)
  const [clipboardMenuOpen, setClipboardMenuOpen] = useState(false)
  const [isDraggingFiles, setIsDraggingFiles] = useState(false)
  const captureTextareaRef = useRef<HTMLTextAreaElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const { uploads, uploadFile, cancelUpload, dismissUpload, keepVisible } = useFileUpload(sessionName, hostId)

  useEffect(() => {
    if (!capturedText || !captureTextareaRef.current) return
    captureTextareaRef.current.focus()
    captureTextareaRef.current.select()
  }, [capturedText])

  const sendSequence = useCallback((sequence: string | Uint8Array) => {
    if (typeof sequence === 'string') {
      sendText(sequence)
      return
    }
    sendRawBytes(sequence)
  }, [sendRawBytes, sendText])

  const sendArrow = useCallback((direction: GestureDirection) => {
    if (direction === 'left') sendSequence('\x1b[D')
    if (direction === 'right') sendSequence('\x1b[C')
    if (direction === 'up') sendSequence('\x1b[A')
    if (direction === 'down') sendSequence('\x1b[B')
  }, [sendSequence])

  const sendPage = useCallback((direction: GestureDirection) => {
    if (direction === 'up') sendSequence('\x1b[5~')
    if (direction === 'down') sendSequence('\x1b[6~')
  }, [sendSequence])

  const handlePaste = useCallback(async () => {
    setClipboardMenuOpen(false)
    try {
      // Async Clipboard API (secure context only) — handles images + text.
      if (navigator.clipboard?.read) {
        const items = await navigator.clipboard.read()
        for (const item of items) {
          const imageType = item.types.find((t) => t.startsWith('image/'))
          if (imageType) {
            const blob = await item.getType(imageType)
            const ext = imageType.split('/')[1] || 'png'
            sendImage(new File([blob], `pasted-image.${ext}`, { type: imageType }), imageType)
            return
          }
        }
        for (const item of items) {
          if (item.types.includes('text/plain')) {
            const blob = await item.getType('text/plain')
            termRef.current?.paste(await blob.text())
            return
          }
        }
        return
      }
      const text = await navigator.clipboard?.readText?.()
      if (text) termRef.current?.paste(text)
    } catch (err) {
      console.error('Failed to paste from clipboard:', err)
    }
  }, [termRef, sendImage])

  const restoreTerminalFocus = useCallback(() => {
    setTimeout(() => focus(), 0)
  }, [focus])

  const handleChooseFile = useCallback(() => {
    setClipboardMenuOpen(false)
    window.addEventListener('focus', restoreTerminalFocus, { once: true })
    fileInputRef.current?.click()
  }, [restoreTerminalFocus])

  const uploadFiles = useCallback(async (files: FileList | File[]) => {
    for (const file of Array.from(files)) {
      const result = await uploadFile(file)
      if (result.quotedPath) {
        if (termConnected) {
          sendText(result.quotedPath)
        } else {
          keepVisible(result.id)
        }
      }
    }
  }, [uploadFile, sendText, termConnected, keepVisible])

  const handleFileSelection = useCallback((event: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(event.target.files ?? [])
    event.target.value = ''
    restoreTerminalFocus()
    if (!files.length) return
    void uploadFiles(files)
  }, [restoreTerminalFocus, uploadFiles])

  const handleTerminalDragEnter = useCallback((event: DragEvent<HTMLDivElement>) => {
    if (!event.dataTransfer.types.includes('Files')) return
    event.preventDefault()
    event.stopPropagation()
    setIsDraggingFiles(true)
  }, [])

  const handleTerminalDragOver = useCallback((event: DragEvent<HTMLDivElement>) => {
    if (!event.dataTransfer.types.includes('Files')) return
    event.preventDefault()
    event.stopPropagation()
    event.dataTransfer.dropEffect = 'copy'
  }, [])

  const handleTerminalDragLeave = useCallback((event: DragEvent<HTMLDivElement>) => {
    if (!event.dataTransfer.types.includes('Files')) return
    if (event.currentTarget.contains(event.relatedTarget as Node)) return
    setIsDraggingFiles(false)
  }, [])

  const handleTerminalDrop = useCallback((event: DragEvent<HTMLDivElement>) => {
    if (!event.dataTransfer.types.includes('Files')) return
    event.preventDefault()
    event.stopPropagation()
    setIsDraggingFiles(false)
    void uploadFiles(event.dataTransfer.files)
    focus()
  }, [focus, uploadFiles])

  const handleCopy = useCallback(() => {
    setClipboardMenuOpen(false)
    const term = termRef.current
    if (!term) return

    const lines: string[] = []
    const buffer = term.buffer.active
    const start = buffer.viewportY
    const end = Math.min(start + term.rows, buffer.length)

    for (let i = start; i < end; i++) {
      const line = buffer.getLine(i)
      if (!line) continue
      lines.push(line.translateToString(true))
    }

    const text = lines.join('\n').trim()
    if (!text) return

    term.clearSelection()
    setCapturedText(text)
  }, [termRef])

  return {
    sendSequence,
    sendArrow,
    sendPage,
    handlePaste,
    handleChooseFile,
    handleFileSelection,
    uploadFiles,
    handleTerminalDragEnter,
    handleTerminalDragOver,
    handleTerminalDragLeave,
    handleTerminalDrop,
    handleCopy,
    clipboardMenuOpen,
    setClipboardMenuOpen,
    capturedText,
    setCapturedText,
    isDraggingFiles,
    setIsDraggingFiles,
    fileInputRef,
    captureTextareaRef,
    uploads,
    cancelUpload,
    dismissUpload,
  }
}
