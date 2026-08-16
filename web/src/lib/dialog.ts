import { createDiscreteApi } from 'naive-ui'

const { dialog } = createDiscreteApi(['dialog'])

export function confirmAction(content: string, options: { title?: string; positiveText?: string; danger?: boolean } = {}) {
  return new Promise<boolean>((resolve) => {
    let settled = false
    const finish = (value: boolean) => {
      if (settled) return
      settled = true
      resolve(value)
    }
    const open = options.danger ? dialog.error : dialog.warning
    open({
      title: options.title ?? '确认操作',
      content,
      positiveText: options.positiveText ?? '确认',
      negativeText: '取消',
      maskClosable: false,
      onPositiveClick: () => finish(true),
      onNegativeClick: () => finish(false),
      onClose: () => finish(false),
    })
  })
}
