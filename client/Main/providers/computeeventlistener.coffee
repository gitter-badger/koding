class ComputeEventListener extends KDObject

  constructor:(options = {})->

    super
      interval : options.interval ? 4000

    @kloud          = KD.singletons.kontrol.getKite
      name          : "kloud"
      environment   : "vagrant"

    @listeners      = []
    @tickInProgress = no
    @running        = no
    @timer          = null


  start:->

    return  if @running
    @running = yes

    @tick()
    @timer = KD.utils.repeat @getOption('interval'), @bound 'tick'


  stop:->

    return  unless @running
    @running = no
    KD.utils.killWait @timer


  addListener:(type, eventId)->

    @listeners.push { type, eventId }
    @start()  unless @running


  tick:->

    return  unless @listeners.length
    return  if @tickInProgress
    @tickInProgress = yes

    {computeController} = KD.singletons
    @kloud.event(@listeners)

    .then (responses)=>

      activeListeners = []
      responses.forEach (res)=>

        if res.err
          warn "Error on '#{res.event_id}':", res.err

        else

          [type, eventId] = res.event.eventId.split '-'

          if res.event.percentage < 100
            activeListeners.push { type, eventId }

          log "#{res.event.eventId}", res.event

          if res.event.percentage is 100 and type is "build"
            computeController.emit "machineBuildCompleted", machineId: eventId

          computeController.emit "public-#{eventId}", res.event
          computeController.emit "#{res.event.eventId}", res.event

      @listeners = activeListeners
      @tickInProgress = no

    .catch (err)=>

      @tickInProgress = no
      warn "Eventer error:", err
      @stop()
