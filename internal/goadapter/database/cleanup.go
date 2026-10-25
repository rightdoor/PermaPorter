package database

// 清理任务的对账查询：GOPROXY 语义下清理只做「复位丢失产物的状态、
// 删除无 DB 记录的孤立文件」，绝不删除仅元数据模块（@v/list / @latest 缓存合法且有用）。
