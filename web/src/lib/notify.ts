import { createDiscreteApi } from 'naive-ui'

const { notification } = createDiscreteApi(['notification'])

const options = { placement: 'top-right' as const, keepAliveOnHover: true }

export const notify = {
  success(content: string, title = '操作成功') {
    notification.success({ ...options, title, content, duration: 3500 })
  },
  error(content: string, title = '操作失败') {
    notification.error({ ...options, title, content, duration: 6000 })
  },
  warning(content: string, title = '请注意') {
    notification.warning({ ...options, title, content, duration: 5000 })
  },
}
