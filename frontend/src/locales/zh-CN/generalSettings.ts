export default {
  pageDescription: '对全部用户生效的实例级设置。',
  retention: {
    title: '请求日志保留',
    desc: '请求日志（记录、请求体与流式文件）的保留期限，超期数据由后台任务小批量渐进删除。',
    days: '保留天数',
    daysTip:
      '0 表示永久保留。设为 N 天则删除超过 N 天的日志，每轮限量删除——下一轮即生效，无需重启服务。',
    daysRequired: '请输入保留天数（0 表示永久保留）',
    daysWholeNumber: '保留天数必须是整数天',
    daysMin: '保留天数不能为负数——永久保留请填 0',
    daysMax: '保留天数不能大于 3650 天（约十年）',
    saved: '已保存',
    loadFailed: '加载当前设置失败',
    retry: '重试',
    conflict: '该设置已被他人修改，已重新加载最新版本，请确认后再次保存。',
  },
}
