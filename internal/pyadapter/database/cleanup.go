package database

// 清理任务的对账查询：Simple 协议语义下清理只做「复位丢失本体的缓存状态、
// 删除无 DB 记录的孤立文件」，绝不删除锚点页记录与仅元数据项目
//（/simple/<project>/ 缓存合法且有用——pip 只查锚点页即可拿到可用版本列表）。
// 具体实现见 pyadapter/cleanup（与 go/npm 清理器语义独立，不得通用化）。
