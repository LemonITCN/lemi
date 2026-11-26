package conns

// Invalidate 同步关闭连接的连接池与控制台会话（备份还原替换了 SQLite 文件后，旧句柄不能再用）。
func (s *Service) Invalidate(id string) {
	s.sessions.closeConn(id)
	s.mu.Lock()
	e := s.pools[id]
	delete(s.pools, id)
	s.mu.Unlock()
	if e != nil {
		e.conn.Close()
		if e.dialer != nil {
			e.dialer.Close()
		}
	}
}
