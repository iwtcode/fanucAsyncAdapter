package fanuc

// soft вызывает необязательную функцию FOCAS: её ошибка (кроме потери
// соединения) не прерывает чтение, а записывается в info.Warnings.
// Возвращает true, если вызов удался.
func (s *session) soft(info *ProgramInfo, name string, args ...uintptr) (bool, error) {
	err := s.call(name, args...)
	if err == nil {
		return true, nil
	}
	if IsConnectionError(err) {
		return false, err
	}
	info.Warnings = append(info.Warnings, err)
	return false, nil
}

// programInfo собирает сведения о выполняемой программе.
func (s *session) programInfo() (*ProgramInfo, error) {
	st, err := s.status()
	if err != nil {
		return nil, err
	}
	info := &ProgramInfo{Status: st}
	b := s.buf

	// Имя выполняемой программы
	b.exe = odbExePrg{}
	ok, err := s.soft(info, "cnc_exeprgname", ptr(&b.exe))
	if err != nil {
		return nil, err
	}
	if ok {
		info.Name = cString(b.exe.Name[:])
		info.Number = int(b.exe.ONum)
	}

	// Номера выполняемой и главной программ
	b.pro = odbPro{}
	if ok, err = s.soft(info, "cnc_rdprgnum", ptr(&b.pro)); err != nil {
		return nil, err
	}
	if ok {
		info.RunningNumber = int(b.pro.Data)
		info.MainNumber = int(b.pro.MData)
	}

	// Номер кадра (N)
	b.seq = odbSeq{}
	if ok, err = s.soft(info, "cnc_rdseqnum", ptr(&b.seq)); err != nil {
		return nil, err
	}
	if ok {
		info.Sequence = int(b.seq.Data)
	}

	// Выполняемый кадр + кадры упреждения
	clear(b.execBuf[:])
	b.execLen = execBufSize - 1
	b.execBlk = 0
	if ok, err = s.soft(info, "cnc_rdexecprog",
		ptr(&b.execLen), ptr(&b.execBlk), ptr(&b.execBuf[0])); err != nil {
		return nil, err
	}
	if ok {
		n := int(b.execLen)
		if n > execBufSize {
			n = execBufSize
		}
		text := normalizeText(cString(b.execBuf[:n]))
		info.CurrentBlock, info.NextBlocks = splitBlocks(text)
	}

	return info, nil
}
