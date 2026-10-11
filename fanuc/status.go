package fanuc

// status читает общее состояние станка (cnc_statinfo).
func (s *session) status() (Status, error) {
	b := s.buf
	b.st = odbSt{}
	if err := s.call("cnc_statinfo", ptr(&b.st)); err != nil {
		return Status{}, err
	}
	return Status{
		Mode:      Mode(b.st.Aut),
		Run:       RunState(b.st.Run),
		Motion:    MotionState(b.st.Motion),
		Alarm:     AlarmState(b.st.Alarm),
		Emergency: b.st.Emergency == 1,
	}, nil
}
